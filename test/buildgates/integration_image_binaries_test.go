package buildgates

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// integrationStage is the CI image stage the containerized integration legs
// run in (`make docker-build-it`, then `make integration-test IT_IMAGE=...`).
const integrationStage = "integration"

// integrationBinary is one external program an integration test execs, and
// the apk package that puts it on PATH.
type integrationBinary struct {
	binary  string
	pkg     string
	callers string
}

// integrationBinaries is every external binary the integration tests exec
// other than the qrdb binaries they build, and go, which the Go base image
// provides. A test that execs a new program adds it here, so the image that
// runs the test carries it.
var integrationBinaries = []integrationBinary{
	{
		binary:  "bash",
		pkg:     "bash",
		callers: "TestCompletion_CLI_ProducesAScriptPerShell, TestQrdbdCompletion_CLI_ProducesAScriptPerShell (bash -n)",
	},
}

// stagePackages returns every apk package the named stage installs, its own
// and those of the stages it is built from.
func stagePackages(stages map[string]*dockerStage, name string) []string {
	var pkgs []string
	for seen := 0; seen <= len(stages); seen++ {
		s, ok := stages[name]
		if !ok {
			break
		}
		pkgs = append(pkgs, s.packages...)
		name = s.from
	}
	return pkgs
}

// integrationImageDefects reports each required binary the integration stage
// of the Dockerfile src does not install, and a missing integration stage.
func integrationImageDefects(src string, required []integrationBinary) []string {
	stages := dockerStages(src)
	if _, ok := stages[integrationStage]; !ok {
		return []string{"no `" + integrationStage + "` stage: the gate has nothing to check"}
	}
	pkgs := stagePackages(stages, integrationStage)
	var defects []string
	for _, b := range required {
		if !slices.Contains(pkgs, b.pkg) {
			defects = append(defects, "the `"+integrationStage+"` stage does not install "+b.pkg+
				", which puts "+b.binary+" on PATH for "+b.callers)
		}
	}
	return defects
}

func TestIntegrationImageCarriesEveryExecedBinary(t *testing.T) {
	if len(integrationBinaries) == 0 || integrationBinaries[0].binary != "bash" {
		t.Fatal("integrationBinaries must declare bash first")
	}
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ciImageDockerfile))
	if err != nil {
		t.Fatalf("read %s: %v", ciImageDockerfile, err)
	}
	for _, d := range integrationImageDefects(string(data), integrationBinaries) {
		t.Errorf("%s: %s", ciImageDockerfile, d)
	}
}

func TestIntegrationImageDefectsFixtures(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		defects int
	}{
		{
			name:    "no integration stage",
			src:     "FROM golang AS base\nRUN apk add --no-cache bash\n",
			defects: 1,
		},
		{
			name:    "bash only in a sibling stage",
			src:     "FROM golang AS base\nFROM base AS toolchain\nRUN apk add --no-cache bash make\nFROM base AS integration\n",
			defects: 1,
		},
		{
			name:    "bash inherited from base",
			src:     "FROM golang AS base\nRUN apk add --no-cache gcc bash\nFROM base AS integration\n",
			defects: 0,
		},
		{
			name:    "bash in the integration stage itself",
			src:     "FROM golang AS base\nFROM base AS integration\nRUN apk add --no-cache \\\n    bash\n",
			defects: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := integrationImageDefects(tc.src, integrationBinaries); len(got) != tc.defects {
				t.Errorf("defects = %q, want %d", got, tc.defects)
			}
		})
	}
}

func TestStagePackages(t *testing.T) {
	stages := dockerStages("FROM golang AS base\nRUN apk add --no-cache gcc\nFROM base AS integration\nRUN apk add --no-cache bash\n")
	if got := stagePackages(stages, integrationStage); !slices.Equal(got, []string{"bash", "gcc"}) {
		t.Errorf("stagePackages(integration) = %q, want [bash gcc]", got)
	}
	if got := stagePackages(stages, "missing"); len(got) != 0 {
		t.Errorf("stagePackages(missing) = %q, want none", got)
	}
}
