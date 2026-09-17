package kube

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/watch"
)

// varRunArgo is the emptyDir Argo mounts at /var/run/argo; debug containers
// need it mounted to reach the pause marker files.
const varRunArgoVolume = "var-run-argo"

// PodSummary is a compact pod listing entry.
type PodSummary struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Phase      string            `json:"phase"`
	Node       string            `json:"node"`
	Containers []string          `json:"containers"`
	Labels     map[string]string `json:"labels,omitempty"`
	Created    time.Time         `json:"created"`
}

// PodDetail describes a single pod for the shell page.
type PodDetail struct {
	Name                string          `json:"name"`
	Namespace           string          `json:"namespace"`
	Phase               string          `json:"phase"`
	Node                string          `json:"node"`
	Containers          []ContainerInfo `json:"containers"`
	EphemeralContainers []ContainerInfo `json:"ephemeralContainers,omitempty"`
	HasVarRunArgo       bool            `json:"hasVarRunArgo"`
	WorkflowName        string          `json:"workflowName,omitempty"`
	NodeID              string          `json:"nodeId,omitempty"`
}

// ContainerInfo is a container's name and running state.
type ContainerInfo struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
}

// ListPods returns pods in a namespace.
func (c *Clients) ListPods(ctx context.Context, ns string) ([]PodSummary, error) {
	list, err := c.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]PodSummary, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		names := make([]string, 0, len(p.Spec.Containers))
		for _, ct := range p.Spec.Containers {
			names = append(names, ct.Name)
		}
		out = append(out, PodSummary{
			Name:       p.Name,
			Namespace:  p.Namespace,
			Phase:      string(p.Status.Phase),
			Node:       p.Spec.NodeName,
			Containers: names,
			Labels:     p.Labels,
			Created:    p.CreationTimestamp.Time,
		})
	}
	return out, nil
}

// PodsByNodeID returns node-id -> pod name for the pods currently owned by a
// workflow (label workflows.argoproj.io/workflow, annotation node-id). Deleted
// pods are simply absent, letting the caller compute their v2 name.
func (c *Clients) PodsByNodeID(ctx context.Context, ns, workflow string) (map[string]string, error) {
	list, err := c.Typed.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "workflows.argoproj.io/workflow=" + workflow,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		if nodeID := p.Annotations["workflows.argoproj.io/node-id"]; nodeID != "" {
			out[nodeID] = p.Name
		}
	}
	return out, nil
}

// GetPodDetail returns detail for one pod.
func (c *Clients) GetPodDetail(ctx context.Context, ns, name string) (*PodDetail, error) {
	p, err := c.Typed.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return podDetail(p), nil
}

func podDetail(p *v1.Pod) *PodDetail {
	running := map[string]bool{}
	ready := map[string]bool{}
	for _, cs := range p.Status.ContainerStatuses {
		running[cs.Name] = cs.State.Running != nil
		ready[cs.Name] = cs.Ready
	}
	for _, cs := range p.Status.EphemeralContainerStatuses {
		running[cs.Name] = cs.State.Running != nil
	}

	d := &PodDetail{
		Name:      p.Name,
		Namespace: p.Namespace,
		Phase:     string(p.Status.Phase),
		Node:      p.Spec.NodeName,
	}
	for _, ct := range p.Spec.Containers {
		d.Containers = append(d.Containers, ContainerInfo{
			Name: ct.Name, Image: ct.Image, Running: running[ct.Name], Ready: ready[ct.Name],
		})
	}
	for _, ec := range p.Spec.EphemeralContainers {
		d.EphemeralContainers = append(d.EphemeralContainers, ContainerInfo{
			Name: ec.Name, Image: ec.Image, Running: running[ec.Name],
		})
	}
	for _, vol := range p.Spec.Volumes {
		if vol.Name == varRunArgoVolume {
			d.HasVarRunArgo = true
		}
	}
	d.WorkflowName = p.Labels["workflows.argoproj.io/workflow"]
	d.NodeID = p.Annotations["workflows.argoproj.io/node-id"]
	return d
}

// DebugRequest asks for an ephemeral debug container.
type DebugRequest struct {
	Namespace string
	Pod       string
	Target    string
	Image     string
}

// EnsureDebugContainer attaches an ephemeral container targeting Target (or
// reuses a running stepshell-* one already targeting it) and waits for it to
// run. It mirrors the target's volume mounts (minus SA token projections) and
// mounts var-run-argo when present, so both the target's filesystem and Argo's
// marker files are reachable. Returns the debug container name.
func (c *Clients) EnsureDebugContainer(ctx context.Context, req DebugRequest) (string, error) {
	pod, err := c.Typed.CoreV1().Pods(req.Namespace).Get(ctx, req.Pod, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	if name := runningDebugFor(pod, req.Target); name != "" {
		return name, nil
	}

	var targetSpec *v1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == req.Target {
			targetSpec = &pod.Spec.Containers[i]
			break
		}
	}
	if targetSpec == nil {
		return "", fmt.Errorf("container %q not found in pod %q", req.Target, req.Pod)
	}

	name := "stepshell-" + rand.String(5)
	ec := v1.EphemeralContainer{
		EphemeralContainerCommon: v1.EphemeralContainerCommon{
			Name:         name,
			Image:        req.Image,
			Command:      []string{"tail", "-f", "/dev/null"},
			TTY:          true,
			Stdin:        true,
			VolumeMounts: debugMounts(pod, targetSpec),
		},
		TargetContainerName: req.Target,
	}
	pod.Spec.EphemeralContainers = append(pod.Spec.EphemeralContainers, ec)

	if _, err := c.Typed.CoreV1().Pods(req.Namespace).UpdateEphemeralContainers(ctx, req.Pod, pod, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("attaching debug container: %w", err)
	}
	if err := c.waitEphemeralRunning(ctx, req.Namespace, req.Pod, name, 60*time.Second); err != nil {
		return "", err
	}
	return name, nil
}

func runningDebugFor(pod *v1.Pod, target string) string {
	targeting := map[string]string{}
	for _, ec := range pod.Spec.EphemeralContainers {
		if strings.HasPrefix(ec.Name, "stepshell-") && ec.TargetContainerName == target {
			targeting[ec.Name] = ec.Name
		}
	}
	for _, cs := range pod.Status.EphemeralContainerStatuses {
		if _, ok := targeting[cs.Name]; ok && cs.State.Running != nil {
			return cs.Name
		}
	}
	return ""
}

// debugMounts copies the target's volume mounts, dropping the projected
// ServiceAccount token, and adds var-run-argo when the pod has it.
func debugMounts(pod *v1.Pod, target *v1.Container) []v1.VolumeMount {
	var mounts []v1.VolumeMount
	hasArgo := false
	for _, m := range target.VolumeMounts {
		if strings.HasPrefix(m.MountPath, "/var/run/secrets/kubernetes.io/serviceaccount") {
			continue
		}
		if m.Name == varRunArgoVolume {
			hasArgo = true
		}
		mounts = append(mounts, m)
	}
	if !hasArgo {
		for _, vol := range pod.Spec.Volumes {
			if vol.Name == varRunArgoVolume {
				mounts = append(mounts, v1.VolumeMount{Name: varRunArgoVolume, MountPath: "/var/run/argo"})
			}
		}
	}
	return mounts
}

func (c *Clients) waitEphemeralRunning(ctx context.Context, ns, pod, name string, timeout time.Duration) error {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	w, err := c.Typed.CoreV1().Pods(ns).Watch(wctx, metav1.SingleObject(metav1.ObjectMeta{Name: pod}))
	if err != nil {
		return err
	}
	defer w.Stop()
	for {
		select {
		case <-wctx.Done():
			return fmt.Errorf("timed out waiting for debug container %q to start", name)
		case ev, ok := <-w.ResultChan():
			if !ok {
				return fmt.Errorf("watch closed while waiting for debug container %q", name)
			}
			if ev.Type == watch.Error {
				return fmt.Errorf("watch error waiting for debug container %q", name)
			}
			p, ok := ev.Object.(*v1.Pod)
			if !ok {
				continue
			}
			for _, cs := range p.Status.EphemeralContainerStatuses {
				if cs.Name == name {
					if cs.State.Running != nil {
						return nil
					}
					if cs.State.Terminated != nil {
						return fmt.Errorf("debug container %q terminated: %s", name, cs.State.Terminated.Reason)
					}
				}
			}
		}
	}
}

// ReleasePause creates the Argo debug-pause marker file
// /var/run/argo/ctr/<container>/<stage>. It first tries `touch` inside the
// container; if the image has no shell it attaches (or reuses) a debug
// container with var-run-argo mounted and touches from there.
// The returned string reports which path was taken ("direct" or "debug").
func (c *Clients) ReleasePause(ctx context.Context, ns, pod, container, stage string) (string, error) {
	if stage != "before" && stage != "after" {
		return "", fmt.Errorf("stage must be before or after, got %q", stage)
	}
	marker := fmt.Sprintf("/var/run/argo/ctr/%s/%s", container, stage)

	if err := c.touch(ctx, ns, pod, container, marker); err == nil {
		return "direct", nil
	} else if !isNoShell(err) {
		return "", err
	}

	dbg, err := c.EnsureDebugContainer(ctx, DebugRequest{
		Namespace: ns, Pod: pod, Target: container, Image: "busybox:stable",
	})
	if err != nil {
		return "", fmt.Errorf("no shell in container and debug attach failed: %w", err)
	}
	if err := c.touch(ctx, ns, pod, dbg, marker); err != nil {
		return "", fmt.Errorf("touch via debug container: %w", err)
	}
	return "debug", nil
}

func (c *Clients) touch(ctx context.Context, ns, pod, container, path string) error {
	var stderr bytes.Buffer
	_, err := c.Exec(ctx, ExecRequest{
		Namespace: ns, Pod: pod, Container: container,
		Command: []string{"sh", "-c", "touch " + path},
		TTY:     false,
		Streams: ExecStreams{Stderr: &stderr},
	})
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// isNoShell reports whether err looks like "no /bin/sh in the image".
func isNoShell(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "executable file not found") ||
		strings.Contains(s, "no such file or directory") ||
		strings.Contains(s, "OCI runtime exec failed")
}

// IsNotFound reports whether err is a Kubernetes NotFound.
func IsNotFound(err error) bool { return apierrors.IsNotFound(err) }
