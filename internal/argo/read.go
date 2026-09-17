package argo

import (
	"context"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// WorkflowSummary is a compact workflow listing entry.
type WorkflowSummary struct {
	Name       string    `json:"name"`
	Namespace  string    `json:"namespace"`
	Phase      string    `json:"phase"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Message    string    `json:"message,omitempty"`
}

// WorkflowNode is one node (step) of a workflow, resolved for the UI.
type WorkflowNode struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	DisplayName  string   `json:"displayName"`
	Type         string   `json:"type"`
	TemplateName string   `json:"templateName,omitempty"`
	Phase        string   `json:"phase"`
	PodName      string   `json:"podName,omitempty"`
	PodExists    bool     `json:"podExists"`
	Paused       bool     `json:"paused"`
	Children     []string `json:"children,omitempty"`
}

// WorkflowDetail is a workflow plus its flattened, pod-resolved nodes.
type WorkflowDetail struct {
	Name       string         `json:"name"`
	Namespace  string         `json:"namespace"`
	Phase      string         `json:"phase"`
	Entrypoint string         `json:"entrypoint,omitempty"`
	Nodes      []WorkflowNode `json:"nodes"`
}

// Reader reads workflows through an impersonated dynamic client.
type Reader struct {
	dyn dynamic.Interface
}

// NewReader builds a Reader.
func NewReader(dyn dynamic.Interface) *Reader { return &Reader{dyn: dyn} }

// ListWorkflows returns workflows in a namespace, newest first.
func (r *Reader) ListWorkflows(ctx context.Context, ns string) ([]WorkflowSummary, error) {
	list, err := r.dyn.Resource(WorkflowGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowSummary, 0, len(list.Items))
	for i := range list.Items {
		w := &list.Items[i]
		out = append(out, WorkflowSummary{
			Name:       w.GetName(),
			Namespace:  w.GetNamespace(),
			Phase:      nestedString(w.Object, "status", "phase"),
			StartedAt:  nestedTime(w.Object, "status", "startedAt"),
			FinishedAt: nestedTime(w.Object, "status", "finishedAt"),
			Message:    nestedString(w.Object, "status", "message"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// GetWorkflow returns detail for one workflow, resolving each node's pod name.
// livePods maps node-id -> pod name for pods that currently exist (the caller
// builds it from a namespace pod list, keyed by the node-id annotation).
func (r *Reader) GetWorkflow(ctx context.Context, ns, name string, livePods map[string]string) (*WorkflowDetail, error) {
	w, err := r.dyn.Resource(WorkflowGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	v2 := podNameV2(w)

	detail := &WorkflowDetail{
		Name:       w.GetName(),
		Namespace:  w.GetNamespace(),
		Phase:      nestedString(w.Object, "status", "phase"),
		Entrypoint: nestedString(w.Object, "spec", "entrypoint"),
	}

	nodes, _, _ := unstructured.NestedMap(w.Object, "status", "nodes")
	for id, raw := range nodes {
		n, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		node := WorkflowNode{
			ID:           id,
			Name:         asString(n["name"]),
			DisplayName:  asString(n["displayName"]),
			Type:         asString(n["type"]),
			TemplateName: asString(n["templateName"]),
			Phase:        asString(n["phase"]),
		}
		node.Children = asStringSlice(n["children"])

		// Only Pod-type nodes have pods.
		if node.Type == "Pod" {
			if live, ok := livePods[id]; ok && live != "" {
				node.PodName = live
				node.PodExists = true
			} else {
				node.PodName = generatePodName(name, node.Name, node.TemplateName, id, v2)
				node.PodExists = false
			}
			// A running Pod node whose pod still exists is a candidate for a
			// pause release; whether it is actually paused is confirmed on the
			// pod page. We flag Running+exists as "possibly paused".
			node.Paused = node.Phase == "Running" && node.PodExists
		}
		detail.Nodes = append(detail.Nodes, node)
	}
	sort.Slice(detail.Nodes, func(i, j int) bool { return detail.Nodes[i].ID < detail.Nodes[j].ID })
	return detail, nil
}

func podNameV2(w *unstructured.Unstructured) bool {
	if v, ok := w.GetAnnotations()[annotationPodNameVer]; ok {
		return v != "v1"
	}
	return true // v2 is the default
}

// --- unstructured helpers ---

func nestedString(obj map[string]any, fields ...string) string {
	s, _, _ := unstructured.NestedString(obj, fields...)
	return s
}

func nestedTime(obj map[string]any, fields ...string) time.Time {
	s, _, _ := unstructured.NestedString(obj, fields...)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
