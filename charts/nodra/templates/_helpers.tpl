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
{{- define "nodra.secretName" -}}
{{- if and .Values.secrets.existingSecret (and .Values.externalSecret .Values.externalSecret.enabled) -}}
{{- fail "set secrets.existingSecret or externalSecret.enabled, not both" -}}
{{- end -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else if and .Values.externalSecret .Values.externalSecret.enabled -}}
{{- .Values.externalSecret.targetName | default (printf "%s-secrets" (include "nodra.fullname" .)) -}}
{{- else -}}
{{- printf "%s-secrets" (include "nodra.fullname" .) -}}
{{- end -}}
{{- end }}
