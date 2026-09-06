{{- define "nodra.name" -}}nodra{{- end }}
{{- define "nodra.fullname" -}}{{- if .Release.Name }}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{- else }}nodra{{- end }}{{- end }}
{{- define "nodra.labels" -}}
app.kubernetes.io/name: {{ include "nodra.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "nodra.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nodra.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: control-plane
{{- end }}
