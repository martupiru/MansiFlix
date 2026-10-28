# MansiFlix Video Lab

Demo independiente desplegada en `k0s-cluser`, namespace `mansiflix`.
Frontend y API Go en un único proceso; FFmpeg procesa una versión a la vez.
No utiliza ni modifica el operador o el PVC `mansiflix-videos`.

## Abrir la demo desplegada

El port-forward iniciado durante la entrega escucha solo en localhost. Si deja de estar activo:

```sh
export KUBECONFIG=/Users/maurosorbello/Documents/Middleware/kubeconfig
kubectl --context=k0s-cluser -n mansiflix port-forward --address=127.0.0.1 service/mansiflix-video-lab-2c75fbdbb3 8080:8080
```

Abrir http://127.0.0.1:8080. Seleccionar vídeo y pulsar **Subir y procesar**.
La interfaz consulta estados y ofrece un enlace independiente para cada MP4.
Límites: 100 MiB, 60 segundos, hasta 4K y altura mínima 360 px. Formatos:
MP4 (`video/mp4`), MOV (`video/quicktime`), WebM (`video/webm`), MKV (`video/x-matroska`).
Se inspecciona el contenido con ffprobe antes de escribir el original.
Una calidad superior a la altura original se marca `unavailable` y no se amplía.

## Almacenamiento e integridad

- Originales nuevos: `mansiflix-originals/video-lab/<job-id>/source.<ext>`.
- Resultados: `mansiflix-encoded/runs/video-lab/<job-id>/<360|720|1080>.mp4`.
- UUID aleatorio de 128 bits por trabajo; consulta de ausencia por clave exacta antes de crear objetos.
- Identidad y Secret exclusivos con `minio-policy.json`: Get/Put y listado de prefijos demo, sin borrado ni administración.
- El backend usa S3 SigV4 con la biblioteca estándar de Go; no envía credenciales al navegador.
- Verifica original y resultados mediante nueva descarga S3, tamaño y SHA-256.
- Resultados H.264/AAC, `yuv420p`, relación de aspecto conservada y `faststart`.
- Solo se habilita la descarga después de validar FFmpeg, ffprobe y la descarga S3.
- Las descargas pasan por el backend y su identidad limitada; no se publican URLs S3 internas.

Los objetos permanecen en MinIO. El catálogo de trabajos es de la sesión del proceso:
si se reemplaza/reinicia el proceso no se reconstruye la lista de trabajos anteriores.
No se ha implementado catálogo durable, autenticación multiusuario ni reproducción HLS.
La interfaz se expone por localhost; no publicar el servicio directamente sin una fase de seguridad adicional.

## Recursos y entrega del ejecutable

Runtime: imagen ya presente en nodo-5 `mansiflix/video-worker:demo-88bb1d91e247`,
FFmpeg/ffprobe 6.1.2. Cliente S3 integrado en el binario Go del backend.
Sin registry o destino SSH configurado, el binario Linux/amd64 comprimido se entregó en
cinco ConfigMaps nuevos e inmutables (650.000 bytes por parte). El initContainer lo
ensambla en emptyDir y comprueba SHA-256 antes de arrancar. No se ejecuta código
obtenido de fuentes públicas ni se monta almacenamiento heredado.
`deploy.py` conserva los manifiestos renderizados en el directorio local de evidencia.
Para una entrega posterior con registry, `Dockerfile` incorpora el mismo binario en
la imagen FFmpeg existente; la imagen local construida es
`mansiflix/video-lab:demo-2c75fbdbb3`. Esta imagen nueva no se importó a los nodos.

Backend: request 200m CPU/256 MiB; límite 1 CPU/512 MiB; `GOMEMLIMIT=96MiB`;
FFmpeg usa un hilo, preset ultrafast y CRF 23. Cola de dos pendientes y un procesamiento
activo. emptyDir `/work` 1 GiB, `/tmp` 64 MiB; raíz de solo lectura, UID/GID 10001,
sin token de ServiceAccount ni privilegios. Timeout por resolución 5 minutos.

El Job administrativo de creación de la identidad fue autorizado expresamente.
Solo ese Job recibe referencias al Secret administrativo; el Deployment no las recibe.
No se actualizó la identidad worker ni ninguna política existente.

## Compilar y probar

Sin dependencias Go externas. Usar una toolchain existente Go >= 1.23
(las pruebas de esta entrega utilizaron Go 1.26.10 ya almacenado en caché):

```sh
cd deploy/video-lab/app
go test -race ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /tmp/mansiflix-video-lab .
```

Prueba real reproducible: genera un clip sintético nuevo y escribe solo sus claves de demo:

```sh
python3 deploy/video-lab/verify-demo.py --out /tmp/video-lab-check-$(date +%s)
```

Requiere ffmpeg y ffprobe locales ya instalados. No necesita credenciales S3 ni modifica permisos.
La prueba de navegador de la entrega también seleccionó el archivo, subió mediante XHR,
pulsó los tres enlaces y comparó hashes. Un MP4 corrupto fue rechazado con HTTP 400
sin crear trabajo; un fixture de 360p produjo solo 360p y marcó las otras calidades no disponibles.

`provision.py --authorized-new-identity` crea recursos e identidad NUEVOS: requiere
aprobación explícita de permisos; nunca debe ejecutarse como diagnóstico de solo lectura.
`deploy.py` requiere el estado del aprovisionamiento en `/tmp/mansiflix-video-lab-evidence/`;
usa `create` y se detiene ante nombres existentes. No aplicarlo sobre recursos ajenos.
No se ejecutó limpieza: originales, MP4, Jobs y evidencias se conservan.
