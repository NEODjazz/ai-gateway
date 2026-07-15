{{- define "auth.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "auth.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "auth.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "auth.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "auth.selectorLabels" -}}
app.kubernetes.io/name: {{ include "auth.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "auth.image" -}}
{{- if .Values.devMode.enabled }}{{ .Values.devMode.image.repository }}:{{ .Values.devMode.image.tag }}{{ else }}{{ .Values.image.repository }}:{{ .Values.image.tag }}{{ end -}}
{{- end -}}

{{- define "auth.imagePullPolicy" -}}
{{- if .Values.devMode.enabled }}{{ .Values.devMode.image.pullPolicy }}{{ else }}{{ .Values.image.pullPolicy }}{{ end -}}
{{- end -}}

{{- define "auth.devCommand" -}}
{{- if .Values.devMode.enabled }}
command: [go]
args: [run, ./cmd/auth]
workingDir: /workspace/repos/auth
{{- end }}
{{- end -}}

{{- define "auth.devEnv" -}}
{{- if .Values.devMode.enabled }}
- name: HOME
  value: /tmp
- name: GOCACHE
  value: /tmp/go-build
- name: GOMODCACHE
  value: /tmp/go-mod
{{- end }}
{{- end -}}
