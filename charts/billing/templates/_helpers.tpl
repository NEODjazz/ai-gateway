{{- define "billing.name" -}}{{ default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}{{- end }}
{{- define "billing.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}
{{- $name := default .Chart.Name .Values.nameOverride }}{{ if contains $name .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}{{- end }}
{{- define "billing.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "billing.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "billing.selectorLabels" -}}
app.kubernetes.io/name: {{ include "billing.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "billing.image" -}}{{ if .Values.devMode.enabled }}{{ .Values.devMode.image.repository }}:{{ .Values.devMode.image.tag }}{{ else }}{{ .Values.image.repository }}:{{ .Values.image.tag }}{{ end }}{{- end }}
{{- define "billing.imagePullPolicy" -}}{{ if .Values.devMode.enabled }}{{ .Values.devMode.image.pullPolicy }}{{ else }}{{ .Values.image.pullPolicy }}{{ end }}{{- end }}
{{- define "billing.secretName" -}}{{ if .Values.billing.secrets.name }}{{ .Values.billing.secrets.name }}{{ else }}{{ include "billing.fullname" . }}-secrets{{ end }}{{- end }}
{{- define "billing.devCommand" -}}
{{- if .Values.devMode.enabled }}
command: [go]
args: [run, ./cmd/billing]
workingDir: /workspace/repos/billing
{{- end }}
{{- end }}
{{- define "billing.devEnv" -}}
{{- if .Values.devMode.enabled }}
- name: HOME
  value: /tmp
- name: GOCACHE
  value: /tmp/go-build
- name: GOMODCACHE
  value: /tmp/go-mod
{{- end }}
{{- end }}
