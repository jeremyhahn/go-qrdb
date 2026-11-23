package buildgates

// CUSTOMIZE (go-qrdb, #1596): once scripts/ci-image.sh load has verified an
// integration image against docker-build's it_id, every later use of it in
// the job names that ID, never the tag. On a shared self-hosted daemon
// another job can load other bytes under the same tag after the check, so a
// tag would run (Compose, through the Makefile's IT_IMAGE) or scan (docker
// save) whatever the tag names by then.

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	gocodecyaml "github.com/jeremyhahn/go-codec/pkg/yaml"
)

var (
	// itHandOff is a step's write of IT_IMAGE to the job environment.
	itHandOff = regexp.MustCompile(`echo "IT_IMAGE=([^"]*)" >> "\$GITHUB_ENV"`)
	// dockerSave is any docker save in a run script; group 1 is its operand.
	dockerSave = regexp.MustCompile(`docker save -o \S+ (\S+)`)
)

// imageIDProblems lists every use of the integration image in ci.yml that
// does not name the verified image ID.
func imageIDProblems(body []byte) ([]string, error) {
	var wf digestWorkflow
	if err := gocodecyaml.New[digestWorkflow]().Unmarshal(body, &wf); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	var problems []string
	for _, name := range names {
		job := wf.Jobs[name]
		handedOn := false
		for _, s := range job.Steps {
			where := name + "'s step " + strconv.Quote(s.Name)
			for _, m := range itHandOff.FindAllStringSubmatch(s.Run, -1) {
				switch {
				case m[1] != "$ID":
					problems = append(problems, where+" hands on IT_IMAGE="+m[1]+
						"; want $ID, the image ID scripts/ci-image.sh load verified: another job can retag a tag after the check")
				case s.Env["ID"] != "${{ needs."+digestProducer+".outputs.it_id }}":
					problems = append(problems, where+" hands on $ID bound to "+strconv.Quote(s.Env["ID"])+
						"; want "+digestProducer+"'s it_id")
				case !strings.Contains(s.Run, `scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"`):
					problems = append(problems, where+" hands on $ID without loading and checking the image against it")
				default:
					handedOn = true
				}
			}
			for _, m := range dockerSave.FindAllStringSubmatch(s.Run, -1) {
				if m[1] != `"$IT_IMAGE"` {
					problems = append(problems, where+" saves "+m[1]+"; want \"$IT_IMAGE\", the verified image ID")
					continue
				}
				if !handedOn {
					problems = append(problems, where+" saves $IT_IMAGE before any step hands on the verified ID")
				}
				if !strings.Contains(s.Run, "case $IT_IMAGE in sha256:*)") {
					problems = append(problems, where+" saves $IT_IMAGE without refusing a value that is not an image ID")
				}
			}
		}
	}
	return problems, nil
}

func TestCIImageID_IntegrationImageUsesTheVerifiedID(t *testing.T) {
	problems, err := imageIDProblems(readCIWorkflowBytes(t))
	if err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

const imageIDFixture = `on: [push]
jobs:
  scan:
    needs: [docker-build]
    steps:
      - name: Load the integration image
        env:
          IMAGE: ${{ needs.docker-build.outputs.it_image }}
          ID: ${{ needs.docker-build.outputs.it_id }}
        run: |
          scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"
          echo "IT_IMAGE=$ID" >> "$GITHUB_ENV"
      - name: trivy image (integration image)
        run: |
          case $IT_IMAGE in sha256:*) ;; *) exit 1 ;; esac
          docker save -o it-image.tar "$IT_IMAGE"
`

func TestCIImageID_FixtureIsValid(t *testing.T) {
	problems, err := imageIDProblems([]byte(imageIDFixture))
	if err != nil || len(problems) != 0 {
		t.Fatalf("fixture: err=%v problems=%v", err, problems)
	}
}

// TestCIImageID_CanFail: each retag-exposed form must redden the gate.
func TestCIImageID_CanFail(t *testing.T) {
	for _, tc := range []struct{ name, old, new string }{
		{name: "the tag handed on", old: `"IT_IMAGE=$ID"`, new: `"IT_IMAGE=$IMAGE"`},
		{name: "an ID from elsewhere", old: "ID: ${{ needs.docker-build.outputs.it_id }}", new: "ID: ${{ needs.docker-build.outputs.toolchain_id }}"},
		{name: "an ID handed on without the load", old: `          scripts/ci-image.sh load "$RUNNER_TEMP/it-image" "$IMAGE" "$ID"` + "\n", new: ""},
		{name: "the tag saved", old: `docker save -o it-image.tar "$IT_IMAGE"`, new: `docker save -o it-image.tar "$IMAGE"`},
		{name: "a save with no ID check", old: "          case $IT_IMAGE in sha256:*) ;; *) exit 1 ;; esac\n", new: ""},
		{name: "a save before the hand-on", old: `          echo "IT_IMAGE=$ID" >> "$GITHUB_ENV"` + "\n", new: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(imageIDFixture, tc.old) {
				t.Fatalf("the fixture has no %q: the case tests nothing", tc.old)
			}
			problems, err := imageIDProblems([]byte(strings.Replace(imageIDFixture, tc.old, tc.new, 1)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(problems) == 0 {
				t.Fatalf("%s passed the gate", tc.name)
			}
		})
	}
}

// pushCall is a scripts/ci-image.sh push in a run script; group 1 is the
// image variable, group 2 the repository name, group 3 the ID variable.
var pushCall = regexp.MustCompile(`scripts/ci-image\.sh push "\$(\w+)" (\S+)(?: "\$(\w+)")?`)

// producerIDFamilies maps the producer's image variable to the save step's
// ID output of that family.
var producerIDFamilies = map[string]string{
	"TOOLCHAIN_IMAGE": "toolchain_id",
	"IT_IMAGE":        "it_id",
}

// producerIDProblems lists every way docker-build publishes an image by its
// mutable content-hash tag rather than by the ID it resolved once (#1597): on
// a shared daemon another run can retag between the save and the push. Each
// push names its family's ID from the save step; no step saves or pushes
// with docker directly (scripts/ci-image.sh save and push archive and push a
// per-run tag created from the ID); an always() step removes the per-run tags.
func producerIDProblems(body []byte) ([]string, error) {
	var wf digestWorkflow
	if err := gocodecyaml.New[digestWorkflow]().Unmarshal(body, &wf); err != nil {
		return nil, err
	}
	job, ok := wf.Jobs[digestProducer]
	if !ok {
		return []string{"ci.yml has no " + digestProducer + " job"}, nil
	}
	var problems []string
	pushes, untags := 0, false
	for _, s := range job.Steps {
		where := digestProducer + "'s step " + strconv.Quote(s.Name)
		// The LAN CI registry push (#1593) runs docker itself, in the
		// canonical script ci_image_digest_test.go pins and gates.
		if s.ID == lanPushID && strings.TrimSpace(s.Run) == lanPushRun {
			continue
		}
		if strings.Contains(s.Run, "docker push") || dockerSave.MatchString(s.Run) {
			problems = append(problems, where+" saves or pushes with docker directly: that names the shared content-hash tag, "+
				"which another run can move; use scripts/ci-image.sh save and push")
		}
		for _, m := range pushCall.FindAllStringSubmatch(s.Run, -1) {
			pushes++
			out, known := producerIDFamilies[m[1]]
			switch {
			case !known:
				problems = append(problems, where+" pushes $"+m[1]+", not a CI image family's tag")
			case m[3] == "":
				problems = append(problems, where+" pushes $"+m[1]+" with no image ID: the push would follow the mutable tag")
			case s.Env[m[3]] != "${{ steps.save.outputs."+out+" }}":
				problems = append(problems, where+" pushes $"+m[1]+" as $"+m[3]+" bound to "+strconv.Quote(s.Env[m[3]])+
					"; want the save step's "+out+", the ID the archive holds")
			}
		}
		if strings.Contains(s.If, "always()") && strings.Contains(s.Run, "scripts/ci-image.sh untag") {
			untags = true
		}
	}
	if pushes == 0 {
		problems = append(problems, digestProducer+" has no scripts/ci-image.sh push")
	}
	if !untags {
		problems = append(problems, digestProducer+" has no always() step running scripts/ci-image.sh untag: the per-run tags would stay")
	}
	return problems, nil
}

func TestCIImageID_ProducerPublishesTheResolvedID(t *testing.T) {
	problems, err := producerIDProblems(readCIWorkflowBytes(t))
	if err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

var producerIDFixture = `on: [push]
jobs:
  docker-build:
    steps:
      - name: Save the devcontainer images
        id: save
        run: |
          id=$(scripts/ci-image.sh save "$TOOLCHAIN_IMAGE" "$RUNNER_TEMP/toolchain-image")
      - name: Push the devcontainer images to GHCR
        id: push
        env:
          TOOLCHAIN_ID: ${{ steps.save.outputs.toolchain_id }}
          IT_ID: ${{ steps.save.outputs.it_id }}
        run: |
          toolchain_ref=$(scripts/ci-image.sh push "$TOOLCHAIN_IMAGE" go-qrdb-toolchain "$TOOLCHAIN_ID")
          it_ref=$(scripts/ci-image.sh push "$IT_IMAGE" go-qrdb-ci-it "$IT_ID")
      - name: Push the devcontainer images to the LAN CI registry
        id: registry
        env:
          TAG: ${{ github.run_id }}-${{ github.run_attempt }}
        run: |
` + "          " + strings.ReplaceAll(lanPushRun, "\n", "\n          ") + `
      - name: Tear down
        if: always()
        run: scripts/ci-image.sh untag "$TOOLCHAIN_IMAGE" $IT_IMAGE
`

func TestCIImageID_ProducerFixtureIsValid(t *testing.T) {
	problems, err := producerIDProblems([]byte(producerIDFixture))
	if err != nil || len(problems) != 0 {
		t.Fatalf("fixture: err=%v problems=%v", err, problems)
	}
}

// TestCIImageID_ProducerCanFail: each retag-exposed producer form must redden
// the gate.
func TestCIImageID_ProducerCanFail(t *testing.T) {
	for _, tc := range []struct{ name, old, new string }{
		{name: "a push by the tag alone", old: ` go-qrdb-ci-it "$IT_ID")`, new: ` go-qrdb-ci-it)`},
		{name: "a push of another family's ID", old: "IT_ID: ${{ steps.save.outputs.it_id }}", new: "IT_ID: ${{ steps.save.outputs.toolchain_id }}"},
		{name: "a docker push of the shared tag", old: "      - name: Tear down\n", new: "      - name: Publish\n        run: docker push \"$TOOLCHAIN_IMAGE\"\n      - name: Tear down\n"},
		{name: "a docker save of the shared tag", old: `id=$(scripts/ci-image.sh save "$TOOLCHAIN_IMAGE" "$RUNNER_TEMP/toolchain-image")`, new: `docker save -o t.tar "$TOOLCHAIN_IMAGE"`},
		{name: "a LAN registry push off the canonical script", old: `lan_push "$IT_IMAGE" it-image it_tag`, new: `docker push "$IT_IMAGE"`},
		{name: "no untag", old: `run: scripts/ci-image.sh untag "$TOOLCHAIN_IMAGE" $IT_IMAGE`, new: "run: true"},
		{name: "an untag that a failure skips", old: "        if: always()\n", new: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(producerIDFixture, tc.old) {
				t.Fatalf("the fixture has no %q: the case tests nothing", tc.old)
			}
			problems, err := producerIDProblems([]byte(strings.Replace(producerIDFixture, tc.old, tc.new, 1)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(problems) == 0 {
				t.Fatalf("%s passed the gate", tc.name)
			}
		})
	}
}

// pruneIf is the condition of the self-hosted CI image retention step.
const pruneIf = "${{ always() && runner.environment != 'github-hosted' }}"

// pruneRun matches the retention step's command; group 1 is its bound in hours.
var pruneRun = regexp.MustCompile(`^scripts/ci-image\.sh prune ([1-9][0-9]*)$`)

// pruneProblems lists every ci.yml job that builds or loads a CI image
// (scripts/ci-image.sh save or load) on a self-hosted runner without the self-hosted retention step
// after it (#1597): a pool daemon outlives the job, and each commit's
// integration image has its own tag, so without it the images accumulate.
// The step runs `scripts/ci-image.sh prune <hours>`, at most 72, under
// always() on self-hosted runners, after the job's last save or load.
func pruneProblems(body []byte) ([]string, error) {
	var wf digestWorkflow
	if err := gocodecyaml.New[digestWorkflow]().Unmarshal(body, &wf); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	var problems []string
	jobs := 0
	for _, name := range names {
		last, pruned := -1, -1
		for i, s := range wf.Jobs[name].Steps {
			// A load only a hosted runner runs (a job on the Docker-free
			// pool) leaves nothing on a pool daemon.
			if (strings.Contains(s.Run, "scripts/ci-image.sh save ") || strings.Contains(s.Run, "scripts/ci-image.sh load ")) &&
				!strings.Contains(s.If, "runner.environment == 'github-hosted'") {
				last = i
			}
			m := pruneRun.FindStringSubmatch(strings.TrimSpace(s.Run))
			if m == nil {
				continue
			}
			where := name + "'s step " + strconv.Quote(s.Name)
			if h, err := strconv.Atoi(m[1]); err != nil || h > 72 {
				problems = append(problems, where+" prunes at "+m[1]+" h; want a bound of at most 72 h")
			}
			if s.If != pruneIf {
				problems = append(problems, where+" runs under "+strconv.Quote(s.If)+"; want "+pruneIf+
					": a failed job must prune too, and a hosted VM is discarded")
				continue
			}
			pruned = i
		}
		if last < 0 {
			continue
		}
		jobs++
		if pruned < last {
			problems = append(problems, name+" builds or loads a CI image with no self-hosted `scripts/ci-image.sh prune` step after it: "+
				"the pool daemon keeps every image")
		}
	}
	if jobs == 0 {
		problems = append(problems, "no job saves or loads a CI image: the gate checks nothing")
	}
	return problems, nil
}

func TestCIImageID_SelfHostedJobsPruneCIImages(t *testing.T) {
	problems, err := pruneProblems(readCIWorkflowBytes(t))
	if err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestCIImageID_PruneFixture(t *testing.T) {
	step := "      - name: Prune old CI images\n        if: " + pruneIf + "\n        run: scripts/ci-image.sh prune 24\n"
	good := imageIDFixture + step
	if problems, err := pruneProblems([]byte(good)); err != nil || len(problems) != 0 {
		t.Fatalf("fixture: err=%v problems=%v", err, problems)
	}
	load := "      - name: Load the integration image\n"
	for name, bad := range map[string]string{
		"no prune":                     imageIDFixture,
		"a prune that a failure skips": strings.Replace(good, pruneIf, "${{ runner.environment != 'github-hosted' }}", 1),
		"a prune before the load":      strings.Replace(imageIDFixture, load, step+load, 1),
		"an unbounded prune":           strings.Replace(good, "prune 24", "prune 9999", 1),
	} {
		t.Run(name, func(t *testing.T) {
			problems, err := pruneProblems([]byte(bad))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(problems) == 0 {
				t.Fatalf("%s passed the gate", name)
			}
		})
	}
}
