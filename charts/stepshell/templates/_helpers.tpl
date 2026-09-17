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
