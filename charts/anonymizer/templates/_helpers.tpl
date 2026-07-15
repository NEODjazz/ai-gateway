{{- define "anonymizer.name" -}}{{ default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}{{- end }}
{{- define "anonymizer.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}
{{- end }}
{{- define "anonymizer.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "anonymizer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "anonymizer.selectorLabels" -}}
app.kubernetes.io/name: {{ include "anonymizer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "anonymizer.image" -}}{{ if .Values.devMode.enabled }}{{ .Values.devMode.image.repository }}:{{ .Values.devMode.image.tag }}{{ else }}{{ .Values.image.repository }}:{{ default .Chart.AppVersion .Values.image.tag }}{{ end }}{{- end }}
{{- define "anonymizer.imagePullPolicy" -}}{{ if .Values.devMode.enabled }}{{ .Values.devMode.image.pullPolicy }}{{ else }}{{ .Values.image.pullPolicy }}{{ end }}{{- end }}
{{- define "anonymizer.configName" -}}{{ include "anonymizer.fullname" . }}-rules{{- end }}
{{- define "anonymizer.devCommand" -}}
{{- if .Values.devMode.enabled }}
command: [go]
args: [run, ./cmd/anonymizer]
workingDir: /workspace/repos/anonymizer
{{- end }}
{{- end }}
{{- define "anonymizer.devEnv" -}}
{{- if .Values.devMode.enabled }}
- name: HOME
  value: /tmp
- name: GOCACHE
  value: /tmp/go-build
- name: GOMODCACHE
  value: /tmp/go-mod
{{- end }}
{{- end }}
