package bench

import (
	"encoding/json"
	"runtime"

	"github.com/Lordymine/codegraph/internal/index"
)

type SampleProvenance struct {
	Go                  string `json:"go"`
	Analysis            string `json:"analysis"`
	SCIPBridge          string `json:"scip_bridge"`
	GoResolver          string `json:"go_resolver"`
	InputsDigest        string `json:"observed_inputs_digest"`
	GoEnvironmentDigest string `json:"go_environment_digest,omitempty"`
	TSEnvironmentDigest string `json:"ts_environment_digest,omitempty"`
	SCIPRSSMethod       string `json:"scip_rss_method"`
}

func sampleProvenance(database string) (SampleProvenance, error) {
	manifest, err := index.ReadManifest(database)
	if err != nil {
		return SampleProvenance{}, err
	}
	inputs, err := json.Marshal(struct {
		Config   []index.InputFingerprint
		Resolver index.ResolverInputPlan
	}{manifest.Inputs, manifest.ResolverInputs})
	if err != nil {
		return SampleProvenance{}, err
	}
	rssMethod := "unavailable on this platform; zero is not a measured zero"
	if runtime.GOOS == "linux" {
		rssMethod = "sampled SCIP process-tree RSS; zero means no successful sample"
	}
	return SampleProvenance{Go: runtime.Version(), Analysis: manifest.AnalysisVersion, SCIPBridge: manifest.SCIPResolverVersion, GoResolver: manifest.GoResolverVersion, InputsDigest: digestBytes(inputs), GoEnvironmentDigest: manifest.GoEnvironment.Digest, TSEnvironmentDigest: manifest.TSEnvironment.Digest, SCIPRSSMethod: rssMethod}, nil
}
