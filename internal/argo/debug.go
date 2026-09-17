package argo

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/rand"
)

// DebugSubmitRequest asks to re-run a workflow with debug-pause breakpoints.
type DebugSubmitRequest struct {
	Namespace   string
	Name        string
	Stage       string   // "before" | "after" | "both"
	Templates   []string // template names to instrument; empty = all
	DebugTTL    int64
	RequestedBy string
}

// DebugSubmitResult reports the created workflow and any caveats.
type DebugSubmitResult struct {
	Name     string   `json:"name"`
	Warnings []string `json:"warnings,omitempty"`
}

// SubmitDebugRerun clones a workflow, injects ARGO_DEBUG_PAUSE_* into the
// selected templates, forces podGC to OnWorkflowCompletion so paused pods
// survive, and creates the new workflow. A top-level workflowTemplateRef is
// resolved and inlined so the env lands on the template container (Argo lifts
// activeDeadlineSeconds only when it sees the env on a template container).
func (r *Reader) SubmitDebugRerun(ctx context.Context, req DebugSubmitRequest) (*DebugSubmitResult, error) {
	before := req.Stage == "before" || req.Stage == "both"
	after := req.Stage == "after" || req.Stage == "both"
	if !before && !after {
		return nil, fmt.Errorf("stage must be before, after, or both")
	}

	src, err := r.dyn.Resource(WorkflowGVR).Namespace(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	res := &DebugSubmitResult{}
	spec, _, err := unstructured.NestedMap(src.Object, "spec")
	if err != nil || spec == nil {
		return nil, fmt.Errorf("source workflow has no spec")
	}

	// Resolve and inline a top-level workflowTemplateRef.
	if ref, ok := spec["workflowTemplateRef"].(map[string]any); ok {
		if err := r.inlineTemplateRef(ctx, req.Namespace, spec, ref); err != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("could not inline workflowTemplateRef: %v; falling back to a pod spec patch", err))
			r.applyPodSpecPatchFallback(spec, before, after)
			delete(spec, "workflowTemplateRef")
		} else {
			delete(spec, "workflowTemplateRef")
		}
	}

	// Inject env into the selected templates.
	injected := 0
	if templates, ok := spec["templates"].([]any); ok {
		want := toSet(req.Templates)
		for _, t := range templates {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if len(want) > 0 {
				if _, sel := want[asString(tm["name"])]; !sel {
					continue
				}
			}
			injected += injectPauseIntoTemplate(tm, before, after)
		}
	}
	if injected == 0 && len(req.Templates) > 0 {
		res.Warnings = append(res.Warnings,
			"none of the requested templates had an injectable container; a pod spec patch was applied instead")
		r.applyPodSpecPatchFallback(spec, before, after)
	}

	// Keep paused/finished pods around for the debug run.
	unstructured.SetNestedField(spec, "OnWorkflowCompletion", "podGC", "strategy")
	unstructured.SetNestedField(spec, req.DebugTTL, "ttlStrategy", "secondsAfterCompletion")
	// A workflow-level deadline would kill a paused pod; clear it.
	delete(spec, "activeDeadlineSeconds")

	newWF := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Workflow",
		"metadata": map[string]any{
			"name":      req.Name + "-dbg-" + rand.String(5),
			"namespace": req.Namespace,
			"labels":    map[string]any{"stepshell.io/debug-of": req.Name},
			"annotations": map[string]any{
				"stepshell.io/requested-by": req.RequestedBy,
			},
		},
		"spec": spec,
	}}

	created, err := r.dyn.Resource(WorkflowGVR).Namespace(req.Namespace).Create(ctx, newWF, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating debug workflow: %w", err)
	}
	res.Name = created.GetName()
	return res, nil
}

// inlineTemplateRef merges a referenced (Cluster)WorkflowTemplate's spec into
// the workflow spec: templates, entrypoint and arguments are taken from the
// template where the workflow does not already set them.
func (r *Reader) inlineTemplateRef(ctx context.Context, ns string, spec, ref map[string]any) error {
	name := asString(ref["name"])
	if name == "" {
		return fmt.Errorf("workflowTemplateRef has no name")
	}
	cluster, _, _ := unstructured.NestedBool(ref, "clusterScope")

	var tmplSpec map[string]any
	if cluster {
		obj, err := r.dyn.Resource(ClusterWorkflowTemplateGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		tmplSpec, _, _ = unstructured.NestedMap(obj.Object, "spec")
	} else {
		obj, err := r.dyn.Resource(WorkflowTemplateGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		tmplSpec, _, _ = unstructured.NestedMap(obj.Object, "spec")
	}
	if tmplSpec == nil {
		return fmt.Errorf("template %q has no spec", name)
	}

	if _, ok := spec["templates"]; !ok {
		if t, ok := tmplSpec["templates"]; ok {
			spec["templates"] = t
		}
	}
	if _, ok := spec["entrypoint"]; !ok {
		if e, ok := tmplSpec["entrypoint"]; ok {
			spec["entrypoint"] = e
		}
	}
	if _, ok := spec["arguments"]; !ok {
		if a, ok := tmplSpec["arguments"]; ok {
			spec["arguments"] = a
		}
	}
	return nil
}

// applyPodSpecPatchFallback sets a workflow-level podSpecPatch adding the pause
// env to container "main" — the last-resort path when templates can't be found
// inline (e.g. step-level templateRef).
func (r *Reader) applyPodSpecPatchFallback(spec map[string]any, before, after bool) {
	env := pauseEnvJSON(before, after)
	patch := fmt.Sprintf(`{"containers":[{"name":"main","env":%s}]}`, env)
	spec["podSpecPatch"] = patch
}

// injectPauseIntoTemplate adds the pause env to a template's container,
// script, and containerSet containers. Returns the number of containers
// instrumented.
func injectPauseIntoTemplate(tm map[string]any, before, after bool) int {
	n := 0
	if c, ok := tm["container"].(map[string]any); ok {
		addPauseEnv(c, before, after)
		n++
	}
	if s, ok := tm["script"].(map[string]any); ok {
		addPauseEnv(s, before, after)
		n++
	}
	if cs, ok := tm["containerSet"].(map[string]any); ok {
		if containers, ok := cs["containers"].([]any); ok {
			for _, c := range containers {
				if cm, ok := c.(map[string]any); ok {
					addPauseEnv(cm, before, after)
					n++
				}
			}
		}
	}
	return n
}

// addPauseEnv appends the pause env vars to a container map's "env" list,
// replacing any pre-existing entries of the same name.
func addPauseEnv(container map[string]any, before, after bool) {
	var env []any
	if existing, ok := container["env"].([]any); ok {
		for _, e := range existing {
			if em, ok := e.(map[string]any); ok {
				name := asString(em["name"])
				if name == debugPauseBeforeEnvVar || name == debugPauseAfterEnvVar {
					continue
				}
			}
			env = append(env, e)
		}
	}
	if before {
		env = append(env, map[string]any{"name": debugPauseBeforeEnvVar, "value": "true"})
	}
	if after {
		env = append(env, map[string]any{"name": debugPauseAfterEnvVar, "value": "true"})
	}
	container["env"] = env
}

// pauseEnvJSON renders the pause env list as JSON for a podSpecPatch.
func pauseEnvJSON(before, after bool) string {
	items := ""
	if before {
		items += `{"name":"ARGO_DEBUG_PAUSE_BEFORE","value":"true"}`
	}
	if after {
		if items != "" {
			items += ","
		}
		items += `{"name":"ARGO_DEBUG_PAUSE_AFTER","value":"true"}`
	}
	return "[" + items + "]"
}

func toSet(items []string) map[string]struct{} {
	if len(items) == 0 {
		return nil
	}
	s := make(map[string]struct{}, len(items))
	for _, i := range items {
		s[i] = struct{}{}
	}
	return s
}
