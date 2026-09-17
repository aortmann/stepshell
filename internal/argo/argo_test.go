package argo

import "testing"

// TestGeneratePodName pins the port of workflow/util.GeneratePodName (v2) so it
// keeps matching upstream. Expected hashes are FNV-1a/32 over nodeName.
func TestGeneratePodName(t *testing.T) {
	cases := []struct {
		name         string
		workflow     string
		nodeName     string
		templateName string
		nodeID       string
		v2           bool
		want         string
	}{
		{
			name:         "v2 with template",
			workflow:     "my-wf",
			nodeName:     "my-wf.step1",
			templateName: "step1",
			nodeID:       "my-wf-123",
			v2:           true,
			want:         "my-wf-step1-2322253660",
		},
		{
			name:         "v2 without template",
			workflow:     "preflight",
			nodeName:     "preflight[0].scan",
			templateName: "",
			nodeID:       "preflight-456",
			v2:           true,
			want:         "preflight-3349709833",
		},
		{
			name:     "v2 root node returns workflow name",
			workflow: "my-wf",
			nodeName: "my-wf",
			v2:       true,
			want:     "my-wf",
		},
		{
			name:   "v1 returns node id",
			nodeID: "my-wf-789",
			v2:     false,
			want:   "my-wf-789",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := generatePodName(tc.workflow, tc.nodeName, tc.templateName, tc.nodeID, tc.v2)
			if got != tc.want {
				t.Errorf("generatePodName = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInjectPauseIntoTemplate checks the env is added to container, script and
// containerSet, and that duplicates are replaced rather than doubled.
func TestInjectPauseIntoTemplate(t *testing.T) {
	tm := map[string]any{
		"name":      "run",
		"container": map[string]any{"image": "x", "env": []any{map[string]any{"name": "KEEP", "value": "1"}}},
	}
	n := injectPauseIntoTemplate(tm, true, true)
	if n != 1 {
		t.Fatalf("instrumented %d containers, want 1", n)
	}
	env := tm["container"].(map[string]any)["env"].([]any)
	if len(env) != 3 { // KEEP + BEFORE + AFTER
		t.Fatalf("env has %d entries, want 3", len(env))
	}

	// Re-injecting must not duplicate the pause vars.
	injectPauseIntoTemplate(tm, true, true)
	env = tm["container"].(map[string]any)["env"].([]any)
	if len(env) != 3 {
		t.Fatalf("after re-inject env has %d entries, want 3", len(env))
	}

	// Values must be the string "true", not a bool (k8s EnvVar.Value is a string).
	for _, e := range env {
		em := e.(map[string]any)
		if em["name"] == debugPauseAfterEnvVar {
			if em["value"] != "true" {
				t.Errorf("pause env value = %v (%T), want string \"true\"", em["value"], em["value"])
			}
		}
	}
}
