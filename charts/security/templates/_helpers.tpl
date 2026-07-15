{{- define "security.name" -}}{{ default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}{{- end }}
{{- define "security.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}
{{- $name := default .Chart.Name .Values.nameOverride }}{{ if contains $name .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}{{- end }}
{{- define "security.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "security.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "security.selectorLabels" -}}
app.kubernetes.io/name: {{ include "security.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "security.image" -}}{{ if .root.Values.devMode.enabled }}{{ .root.Values.devMode.image.repository }}:{{ .root.Values.devMode.image.tag }}{{ else }}{{ .component.image.repository }}:{{ default .root.Values.image.tag .component.image.tag }}{{ end }}{{- end }}
{{- define "security.imagePullPolicy" -}}{{ if .root.Values.devMode.enabled }}{{ .root.Values.devMode.image.pullPolicy }}{{ else }}{{ default .root.Values.image.pullPolicy .component.image.pullPolicy }}{{ end }}{{- end }}
{{- define "security.devEnv" -}}
{{- if .Values.devMode.enabled }}
- name: HOME
  value: /tmp
- name: GOCACHE
  value: /tmp/go-build
- name: GOMODCACHE
  value: /tmp/go-mod
{{- end }}
{{- end }}
