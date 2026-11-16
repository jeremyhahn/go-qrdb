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

	"gopkg.in/yaml.v3"
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
	if err := yaml.Unmarshal(body, &wf); err != nil {
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
