{{/* Общие метки всех объектов. */}}
{{- define "fd.labels" -}}
app.kubernetes.io/part-of: food-delivery
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{/* Полное имя образа: fd.image (list $ "order") */}}
{{- define "fd.image" -}}
{{- $root := index . 0 -}}
{{ $root.Values.image.registry }}/{{ $root.Values.image.prefix }}{{ index . 1 }}:{{ $root.Values.image.tag }}
{{- end }}

{{/* Ограничения безопасности контейнера. */}}
{{- define "fd.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
capabilities:
  drop: ["ALL"]
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/* Под быстро переносится с отказавшего узла: вместо 300 секунд по умолчанию - 20. */}}
{{- define "fd.tolerations" -}}
- key: node.kubernetes.io/not-ready
  operator: Exists
  effect: NoExecute
  tolerationSeconds: 20
- key: node.kubernetes.io/unreachable
  operator: Exists
  effect: NoExecute
  tolerationSeconds: 20
{{- end }}

{{- define "fd.pullSecrets" -}}
{{- if .Values.image.pullSecret }}
imagePullSecrets:
  - name: {{ .Values.image.pullSecret }}
{{- end }}
{{- end }}
