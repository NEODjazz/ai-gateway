{{- define "ai-gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "ai-gateway.fullname" -}}
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

{{- define "ai-gateway.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "ai-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "ai-gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ai-gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "ai-gateway.image" -}}
{{- if .Values.devMode.enabled -}}
{{ .Values.devMode.image.repository }}:{{ .Values.devMode.image.tag }}
{{- else -}}
{{ .Values.image.repository }}:{{ default .Chart.AppVersion .Values.image.tag }}
{{- end -}}
{{- end -}}

{{- define "ai-gateway.imagePullPolicy" -}}
{{- if .Values.devMode.enabled -}}
{{ .Values.devMode.image.pullPolicy }}
{{- else -}}
{{ .Values.image.pullPolicy }}
{{- end -}}
{{- end -}}

{{- define "ai-gateway.devCommand" -}}
{{- if .root.Values.devMode.enabled }}
command:
  - go
args:
  - run
  - ./cmd/{{ .service }}
workingDir: /workspace/repos/{{ .service }}
{{- end }}
{{- end -}}

{{- define "ai-gateway.devVolumeMounts" -}}
{{- if .Values.devMode.enabled }}
volumeMounts:
  - name: source
    mountPath: /workspace
{{- end }}
{{- end -}}

{{- define "ai-gateway.devVolumes" -}}
{{- if .Values.devMode.enabled }}
volumes:
  - name: source
    hostPath:
      path: {{ .Values.devMode.sourceHostPath | quote }}
      type: Directory
{{- end }}
{{- end -}}

{{- define "ai-gateway.devEnv" -}}
{{- if .Values.devMode.enabled }}
- name: HOME
  value: /tmp
- name: GOCACHE
  value: /tmp/go-build
- name: GOMODCACHE
  value: /tmp/go-mod
{{- end }}
{{- end -}}

{{- define "ai-gateway.providerConfigName" -}}
{{ include "ai-gateway.fullname" . }}-provider-config
{{- end -}}

{{- define "ai-gateway.providerSecretName" -}}
{{- if .Values.gateway.providerSecrets.name -}}
{{ .Values.gateway.providerSecrets.name }}
{{- else -}}
{{ include "ai-gateway.fullname" . }}-provider-api-keys
{{- end -}}
{{- end -}}

{{- define "ai-gateway.providerApiKeyEnvName" -}}
{{- printf "PROVIDER_API_KEY_%s" (.name | replace "-" "_" | replace "." "_" | replace "/" "_" | upper) -}}
{{- end -}}

{{- define "ai-gateway.integerString" -}}
{{- if kindIs "float64" . -}}
{{- printf "%.0f" . -}}
{{- else -}}
{{- printf "%v" . -}}
{{- end -}}
{{- end -}}
