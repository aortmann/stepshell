{{- define "stepshell.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "stepshell.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "stepshell.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "stepshell.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end -}}

{{- define "stepshell.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stepshell.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "stepshell.serviceAccountName" -}}
{{ include "stepshell.fullname" . }}
{{- end -}}

{{- define "stepshell.secretName" -}}
{{- if .Values.existingSecret -}}
{{ .Values.existingSecret }}
{{- else -}}
{{ include "stepshell.fullname" . }}
{{- end -}}
{{- end -}}

{{/*
Resolve the base64-encoded session key for the Secret's data field.
Precedence: explicit value (assumed already base64), else the one already
stored in the Secret (preserved across upgrades), else a freshly generated
32-byte key. randBytes already returns base64.
*/}}
{{- define "stepshell.sessionKeyB64" -}}
{{- if .Values.session.key -}}
{{ .Values.session.key }}
{{- else -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "stepshell.fullname" .) -}}
{{- if and $existing $existing.data (index $existing.data "session-key") -}}
{{ index $existing.data "session-key" }}
{{- else -}}
{{ randBytes 32 }}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "stepshell.dex.fullname" -}}
{{ include "stepshell.fullname" . }}-dex
{{- end -}}

{{/*
The Dex config.yaml body. Kept in a helper so the ConfigMap and the pod's
checksum annotation share one source (and to avoid self-referential recursion).
Secrets are injected via env and expanded by Dex ($VAR / secretEnv).
*/}}
{{- define "stepshell.dex.config" -}}
{{- $issuer := include "stepshell.oidc.issuer" . -}}
issuer: {{ $issuer }}
storage:
  type: memory
web:
  http: 0.0.0.0:5556
telemetry:
  http: 0.0.0.0:5558
oauth2:
  skipApprovalScreen: true
connectors:
  - type: github
    id: github
    name: GitHub
    config:
      clientID: {{ required "dex.github.clientID is required when dex.enabled" .Values.dex.github.clientID }}
      clientSecret: $GITHUB_CLIENT_SECRET
      redirectURI: {{ $issuer }}/callback
      loadAllGroups: false
      {{- with .Values.dex.github.orgs }}
      orgs:
        {{- range . }}
        - name: {{ .name }}
          {{- with .teams }}
          teams:
            {{- range . }}
            - {{ . }}
            {{- end }}
          {{- end }}
        {{- end }}
      {{- end }}
staticClients:
  - id: stepshell
    name: stepshell
    secretEnv: STEPSHELL_CLIENT_SECRET
    redirectURIs:
      - {{ .Values.baseURL | trimSuffix "/" }}/auth/callback
{{- end -}}

{{/*
The OIDC issuer stepshell verifies against: the bundled Dex public URL when Dex
is enabled, otherwise the explicit oidc.issuer.
*/}}
{{- define "stepshell.oidc.issuer" -}}
{{- if .Values.dex.enabled -}}
{{ required "dex.publicURL is required when dex.enabled" .Values.dex.publicURL | trimSuffix "/" }}
{{- else -}}
{{ .Values.oidc.issuer }}
{{- end -}}
{{- end -}}

{{/* The OIDC client id: fixed "stepshell" for bundled Dex, else oidc.clientID. */}}
{{- define "stepshell.oidc.clientID" -}}
{{- if .Values.dex.enabled -}}stepshell{{- else -}}{{ .Values.oidc.clientID }}{{- end -}}
{{- end -}}

{{/*
Resolve the stepshell<->Dex shared client secret (base64) for the Secret.
Precedence: explicit dex.clientSecret, else preserved value, else generated.
*/}}
{{- define "stepshell.dex.clientSecretB64" -}}
{{- if .Values.dex.clientSecret -}}
{{ .Values.dex.clientSecret | b64enc }}
{{- else -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "stepshell.fullname" .) -}}
{{- if and $existing $existing.data (index $existing.data "oidc-client-secret") -}}
{{ index $existing.data "oidc-client-secret" }}
{{- else -}}
{{ randAlphaNum 40 | b64enc }}
{{- end -}}
{{- end -}}
{{- end -}}
