{{- define "tyk-sre-assignment.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "tyk-sre-assignment.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Name of the ServiceAccount to use. Falls back to the fullname when the chart
creates it, and to "default" when it does not — so the binding always references
a subject that exists.
*/}}
{{- define "tyk-sre-assignment.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "tyk-sre-assignment.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Selector labels. Kept minimal and stable: these land in the Deployment's
matchLabels, which is immutable after creation, so anything that changes between
releases must not be in here.
*/}}
{{- define "tyk-sre-assignment.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tyk-sre-assignment.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Common labels. Includes the chart version, so these must only be used on
metadata, never in a selector.
*/}}
{{- define "tyk-sre-assignment.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
{{ include "tyk-sre-assignment.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}