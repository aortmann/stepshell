package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/strikesecurity/stepshell/internal/argo"
	"github.com/strikesecurity/stepshell/internal/kube"
	"github.com/strikesecurity/stepshell/internal/session"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// clientsFor builds impersonated clients for the request's identity, writing an
// error response and returning ok=false on failure.
func (s *Server) clientsFor(w http.ResponseWriter, r *http.Request) (*kube.Clients, session.Identity, bool) {
	id := identityFrom(r.Context())
	clients, err := s.factory.For(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "building kube client: "+err.Error())
		return nil, id, false
	}
	return clients, id, true
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"user":        id.User,
		"groups":      id.Groups,
		"email":       id.Email,
		"argoEnabled": s.cfg.Argo.Enabled,
		"argoUIURL":   s.cfg.Argo.UIURL,
	})
}

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	clients, _, ok := s.clientsFor(w, r)
	if !ok {
		return
	}
	list, err := clients.Typed.CoreV1().Namespaces().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		// The user may not be allowed to list namespaces cluster-wide; fall
		// back to the configured list rather than failing the whole UI.
		if apierrors.IsForbidden(err) && len(s.cfg.Namespaces) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"namespaces": s.cfg.Namespaces, "fallback": true})
			return
		}
		writeKubeError(w, err)
		return
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespaces": names})
}

func (s *Server) handleListPods(w http.ResponseWriter, r *http.Request) {
	clients, _, ok := s.clientsFor(w, r)
	if !ok {
		return
	}
	pods, err := clients.ListPods(r.Context(), r.PathValue("ns"))
	if err != nil {
		writeKubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pods": pods})
}

func (s *Server) handleGetPod(w http.ResponseWriter, r *http.Request) {
	clients, _, ok := s.clientsFor(w, r)
	if !ok {
		return
	}
	pod, err := clients.GetPodDetail(r.Context(), r.PathValue("ns"), r.PathValue("pod"))
	if err != nil {
		writeKubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pod)
}

type attachDebugBody struct {
	Target string `json:"target"`
	Image  string `json:"image"`
}

func (s *Server) handleAttachDebug(w http.ResponseWriter, r *http.Request) {
	clients, id, ok := s.clientsFor(w, r)
	if !ok {
		return
	}
	var body attachDebugBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Image == "" {
		body.Image = s.cfg.DebugImage
	}
	ns, pod := r.PathValue("ns"), r.PathValue("pod")
	name, err := clients.EnsureDebugContainer(r.Context(), kube.DebugRequest{
		Namespace: ns, Pod: pod, Target: body.Target, Image: body.Image,
	})
	if err != nil {
		writeKubeError(w, err)
		return
	}
	s.log.Info("debug.attach", "user", id.User, "namespace", ns, "pod", pod, "target", body.Target, "container", name)
	writeJSON(w, http.StatusOK, map[string]any{"container": name})
}

type releaseBody struct {
	Container string `json:"container"`
	Stage     string `json:"stage"`
}

func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	clients, id, ok := s.clientsFor(w, r)
	if !ok {
		return
	}
	var body releaseBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ns, pod := r.PathValue("ns"), r.PathValue("pod")
	via, err := clients.ReleasePause(r.Context(), ns, pod, body.Container, body.Stage)
	if err != nil {
		writeKubeError(w, err)
		return
	}
	s.log.Info("argo.release", "user", id.User, "namespace", ns, "pod", pod, "container", body.Container, "stage", body.Stage, "via", via)
	writeJSON(w, http.StatusOK, map[string]any{"released": true, "via": via})
}

// --- argo ---

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	clients, _, ok := s.clientsFor(w, r)
	if !ok {
		return
	}
	wfs, err := argo.NewReader(clients.Dynamic).ListWorkflows(r.Context(), r.PathValue("ns"))
	if err != nil {
		writeKubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": wfs})
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	clients, _, ok := s.clientsFor(w, r)
	if !ok {
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")

	// node-id -> live pod name, from the pods this workflow currently owns.
	livePods, err := clients.PodsByNodeID(r.Context(), ns, name)
	if err != nil {
		writeKubeError(w, err)
		return
	}

	wf, err := argo.NewReader(clients.Dynamic).GetWorkflow(r.Context(), ns, name, livePods)
	if err != nil {
		writeKubeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

type workflowDebugBody struct {
	Stage     string   `json:"stage"`
	Templates []string `json:"templates"`
}

func (s *Server) handleWorkflowDebug(w http.ResponseWriter, r *http.Request) {
	clients, id, ok := s.clientsFor(w, r)
	if !ok {
		return
	}
	var body workflowDebugBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Stage == "" {
		body.Stage = "after"
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	res, err := argo.NewReader(clients.Dynamic).SubmitDebugRerun(r.Context(), argo.DebugSubmitRequest{
		Namespace: ns, Name: name, Stage: body.Stage, Templates: body.Templates,
		DebugTTL: s.cfg.Argo.DebugTTL, RequestedBy: id.User,
	})
	if err != nil {
		writeKubeError(w, err)
		return
	}
	s.log.Info("argo.debug.submit", "user", id.User, "namespace", ns, "of", name, "new", res.Name, "stage", body.Stage)
	writeJSON(w, http.StatusOK, res)
}

// --- response helpers ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg, "code": code})
}

// writeKubeError maps a Kubernetes API error onto its status code and message,
// so a Forbidden reads exactly like kubectl's.
func writeKubeError(w http.ResponseWriter, err error) {
	var se apierrors.APIStatus
	if errors.As(err, &se) {
		st := se.Status()
		msg := st.Message
		if msg == "" {
			msg = err.Error()
		}
		writeError(w, int(st.Code), msg)
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}
