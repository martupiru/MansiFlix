# MansiFlix Video Lab

Demo independiente de frontend y API Go: FFmpeg procesa una versión a la vez y
los objetos se conservan en MinIO. No utiliza ni modifica el operador o su PVC.
Los scripts no fijan rutas del equipo, contexto ni nodo de un despliegue anterior.

## Configuración portable

Cada opción de CLI tiene una variable de entorno equivalente. La CLI tiene prioridad.
Los scripts preservan `KUBECONFIG` y el comportamiento normal de kubectl. Si se utiliza
`--kubeconfig` o `VIDEO_LAB_KUBECONFIG`, ese archivo se pasa solo a los subprocesos;
no cambia la configuración global ni el entorno de la shell.

| Opción | Variable | Valor por defecto |
|---|---|---|
| `--context` | `VIDEO_LAB_CONTEXT` | Obligatorio: selección explícita |
| `--namespace` | `VIDEO_LAB_NAMESPACE` | `mansiflix` |
| `--kubeconfig` | `VIDEO_LAB_KUBECONFIG` | Heredar `KUBECONFIG`/configuración de kubectl |
| `--node` | `VIDEO_LAB_NODE` | Sin nodeSelector; scheduling normal |
| `--evidence-dir` | `VIDEO_LAB_EVIDENCE_DIR` | Obligatorio |
| `--state-file` | `VIDEO_LAB_STATE_FILE` | `deployment-state.json` dentro de evidence-dir |
| `--binary` (deploy) | `VIDEO_LAB_BINARY` | Obligatorio: ejecutable Linux/amd64 compilado |
| `--endpoint` | `VIDEO_LAB_S3_ENDPOINT` | `http://minio.<namespace>.svc.cluster.local:9000` |
| `--worker-image` (deploy) | `VIDEO_LAB_WORKER_IMAGE` | Imagen FFmpeg revisada `mansiflix/video-worker:demo-88bb1d91e247` |
| `--mc-image` (provision) | `VIDEO_LAB_MC_IMAGE` | Cliente revisado `mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r1` |
| `--image-pull-policy` | `VIDEO_LAB_IMAGE_PULL_POLICY` | `Never`; opciones `IfNotPresent` y `Always` |
| `--admin-secret` (provision) | `VIDEO_LAB_ADMIN_SECRET` | `mansiflix-minio-admin` |

`/work`, `/runtime` y `/tmp` en los manifiestos son rutas **dentro del contenedor**,
respaldadas por emptyDir. No son directorios del equipo que ejecuta los scripts.
La política de buckets sigue siendo específica de MansiFlix; elegir otro namespace
no crea MinIO, Secrets administrativos, buckets ni NetworkPolicies automáticamente.

Preparar un directorio de trabajo y elegir el contexto:

```sh
kubectl config get-contexts
# Solo si no se usa ya un kubeconfig configurado:
# export KUBECONFIG="$HOME/.kube/config"
# Elegir expresamente el contexto que se revisó; este ejemplo usa el actual.
export VIDEO_LAB_CONTEXT="$(kubectl config current-context)"
export VIDEO_LAB_NAMESPACE="mansiflix"
export VIDEO_LAB_WORKSPACE="$(mktemp -d)"
export VIDEO_LAB_EVIDENCE_DIR="$VIDEO_LAB_WORKSPACE/evidence"
export VIDEO_LAB_BINARY="$VIDEO_LAB_WORKSPACE/video-lab"
```

Los scripts comprueban que el contexto elegido existe y que el namespace está Active.
El contexto elegido puede diferir del `current-context`: todas las operaciones usan
`--context` explícitamente y no ejecutan `kubectl config use-context`.

Para imágenes importadas con `Never`, seleccionar con `--node` un nodo donde estén
presentes. Para usar scheduling normal, publicar imágenes accesibles desde los nodos
y elegir una política de descarga adecuada. Una etiqueta en el ejemplo no demuestra
disponibilidad: verificarlo en el entorno destino antes de desplegar.

## Abrir una demo existente

Consultar los nombres de recursos y seleccionar el Service de la ejecución deseada:

```sh
kubectl --context="$VIDEO_LAB_CONTEXT" -n "$VIDEO_LAB_NAMESPACE" get deployments \
  -l app.kubernetes.io/name=mansiflix-video-lab
export VIDEO_LAB_APP="nombre-del-service-de-la-ejecucion"
kubectl --context="$VIDEO_LAB_CONTEXT" -n "$VIDEO_LAB_NAMESPACE" port-forward \
  --address=127.0.0.1 "service/$VIDEO_LAB_APP" 8080:8080
```

`deploy.py` imprime el comando con el nombre real al finalizar. No se debe reutilizar
un identificador de ejecución ajeno. Abrir http://127.0.0.1:8080, seleccionar vídeo y
pulsar **Subir y procesar**. Cada MP4 tiene un enlace independiente.

Límites: 100 MiB, 60 segundos, área hasta 3840×2160 píxeles y altura mínima 360 px.
Formatos: MP4 (`video/mp4`), MOV (`video/quicktime`), WebM (`video/webm`) y MKV
(`video/x-matroska`). Se inspecciona el contenido con ffprobe antes de guardar el
original. Las calidades superiores a la altura original se marcan `unavailable`.

## Compilar y probar sin modificar el clúster

No hay dependencias Go externas. Usar una toolchain existente Go >= 1.23:

```sh
(cd deploy/video-lab/app && go test -race ./...)
(cd deploy/video-lab/app && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags='-s -w' -o "$VIDEO_LAB_BINARY" .)
python3 -m unittest discover -s deploy/video-lab/tests -v
```

Los tests Python simulan kubectl; no crean recursos ni credenciales en Kubernetes.
`--help` no consulta el clúster ni lee el estado de un despliegue.

Si ya existe el archivo de estado de un aprovisionamiento:

```sh
python3 deploy/video-lab/deploy.py --render-only
```

Ese modo solo escribe `manifests.json` en el directorio de evidencia. No invoca
kubectl ni actualiza el estado. Los archivos de estado nuevos registran contexto y
namespace: un destino distinto se rechaza. Los estados anteriores sin esos campos
siguen requiriendo selección explícita del destino por CLI/entorno.

Para consultar opciones sin ninguna operación:

```sh
python3 deploy/video-lab/provision.py --help
python3 deploy/video-lab/deploy.py --help
```

## Crear una ejecución nueva: requiere autorización

Los siguientes comandos **crean recursos y una identidad MinIO**. Requieren
aprobación de permisos y revisión de las imágenes, nodo, endpoint y Secret
administrativo elegidos. No son comandos de diagnóstico de solo lectura.

```sh
# Solo después de esa aprobación; usa el contexto y directorios elegidos arriba.
python3 deploy/video-lab/provision.py --authorized-new-identity
# Esperar que el Job -provision termine correctamente antes de desplegar la API.
python3 deploy/video-lab/deploy.py
```

También se pueden pasar los valores mediante `--context`, `--namespace`,
`--evidence-dir`, `--binary` y las demás opciones sin exportar variables.
`provision.py` se detiene si ya hay un archivo de estado para una ejecución nueva.
Si una ejecución quedó parcial, `--resume-name` conserva sus credenciales e
inspecciona únicamente metadata. No reemplaza recursos ni rota claves.

`deploy.py` usa `create` y se detiene ante un nombre existente. El archivo de estado
registra cada recurso creado para conservar evidencia incluso ante fallos parciales.
No ejecutar sobre recursos ajenos ni aplicar indiscriminadamente una base completa.

## Almacenamiento e integridad

- Originales: `mansiflix-originals/video-lab/<job-id>/source.<ext>`.
- Resultados: `mansiflix-encoded/runs/video-lab/<job-id>/<360|720|1080>.mp4`.
- ID aleatorio de 128 bits por trabajo y comprobación previa de claves exactas.
- Identidad/Secret exclusivos y política `minio-policy.json`, sin borrado ni administración.
- Cliente S3 SigV4 estándar de Go: las credenciales no llegan al navegador.
- Lectura posterior S3 para comparar tamaño y SHA-256 de original y resultados.
- H.264/AAC cuando hay audio, `yuv420p`, relación de aspecto conservada y `faststart`.
- Las descargas pasan por el backend; no se publican URLs S3 internas.

Los objetos permanecen en MinIO, pero el catálogo de trabajos vive en memoria y no
se reconstruye tras reiniciar el proceso. No se implementó catálogo durable,
autenticación multiusuario ni HLS. La interfaz se expone por localhost.

## Runtime y distribución del binario

El initContainer ensambla el ejecutable Linux/amd64 desde ConfigMaps nuevos e
inmutables y comprueba SHA-256 antes de arrancar. Es un mecanismo acotado de
laboratorio cuando no existe un registry configurado. La imagen runtime elegida
necesita FFmpeg/ffprobe. El cliente S3 está integrado en el ejecutable Go.
Para usar un registry, `Dockerfile` puede incorporar ese mismo binario en una imagen.
No se presupone que una imagen local esté disponible en el clúster.

Backend: request 200m CPU/256 MiB; límite 1 CPU/512 MiB; `GOMEMLIMIT=96MiB`.
Un hilo FFmpeg, preset ultrafast y CRF 23. Cola de dos pendientes y un procesamiento
activo. emptyDir de trabajo 1 GiB; raíz de solo lectura, UID/GID 10001, seccomp,
sin capabilities ni token de ServiceAccount. Timeout por resolución: 5 minutos.
Solo el Job de aprovisionamiento autorizado referencia el Secret administrativo;
el Deployment referencia exclusivamente el Secret de Video Lab.

## Prueba real reproducible: crea objetos sintéticos nuevos

Con port-forward activo y ffmpeg/ffprobe locales ya instalados:

```sh
python3 deploy/video-lab/verify-demo.py \
  --url http://127.0.0.1:8080 --out "$VIDEO_LAB_WORKSPACE/check"
```

Genera un fixture y comprueba formato, dimensiones, faststart y hashes de las tres
descargas. No necesita credenciales S3 ni modifica permisos; sí crea objetos de
su propia ejecución mediante la API. No se ejecuta limpieza de objetos anteriores.
