{{- define "dreem.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "dreem.fullname" -}}
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

{{- define "dreem.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "dreem.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/* Selector labels of a component. Usage: include "dreem.selectorLabels" (list . "operator") */}}
{{- define "dreem.selectorLabels" -}}
{{- $root := index . 0 -}}
app.kubernetes.io/name: {{ include "dreem.name" $root }}
app.kubernetes.io/instance: {{ $root.Release.Name }}
app.kubernetes.io/component: {{ index . 1 }}
{{- end }}

{{- define "dreem.componentLabels" -}}
{{ include "dreem.labels" (index . 0) }}
app.kubernetes.io/component: {{ index . 1 }}
{{- end }}

{{- define "dreem.image" -}}
{{- printf "%s:%s" .repository (toString .tag) }}
{{- end }}

{{- define "dreem.operator.serviceAccountName" -}}
{{- printf "%s-operator" (include "dreem.fullname" .) }}
{{- end }}

{{- define "dreem.forecast.serviceAccountName" -}}
{{- printf "%s-forecast" (include "dreem.fullname" .) }}
{{- end }}

{{- define "dreem.metricUpdate.serviceAccountName" -}}
{{- printf "%s-metric-update" (include "dreem.fullname" .) }}
{{- end }}

{{/* Renders a map as ConfigMap data, every value as a quoted string. */}}
{{- define "dreem.stringData" -}}
{{- range $k, $v := . }}
{{ $k }}: {{ toString $v | quote }}
{{- end }}
{{- end }}

{{/* The operator and the forecaster hardcode the "dreem" namespace. */}}
{{- define "dreem.validateNamespace" -}}
{{- if ne .Release.Namespace "dreem" }}
{{- fail (printf "DREEM must be installed in the \"dreem\" namespace (got %q): the operator and the forecaster look up their ConfigMaps, Secrets and custom resources there. Use -n dreem --create-namespace." .Release.Namespace) }}
{{- end }}
{{- end }}
