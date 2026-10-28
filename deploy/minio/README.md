# MinIO académico para MansiFlix

Una instancia, Service interno HTTP/S3 y PVC nuevo exclusivo. Esta configuración
usa datos sintéticos: no es apta para producción, no tiene alta disponibilidad
y no garantiza confidencialidad del tráfico. No se modifica `mansiflix-videos`,
su PV, sus archivos ni los Jobs anteriores. No hay migración ni detach forzado.

## Arquitectura

```text
Job seed: FFmpeg genera un MP4 sintético → bucket mansiflix-originals
Job HLS: descarga S3 → worker existente + FFmpeg → subida S3
MinIO → /data → PVC mansiflix-minio-data (20 GiB, RWO)
```

El Job HLS adapta el worker mediante initContainers y un uploader. No modifica
su código ni el operador desplegado. El encoder recibe SOURCE/RESOLUTION/OUTPUT;
los pasos de transferencia reciben solo el Secret worker. El operador actual
continúa usando su flujo anterior: todavía no genera estos Jobs S3 a partir de
VideoEncoding. La demo preparada procesa 360p; no demuestra adaptación automática
entre calidades, frontend, API de reproducción ni CDN.

Cada intento publica en `mansiflix-encoded/runs/hls-demo/<Pod-UID>/360/`. Primero
sube segmentos y hashes; publica index.m3u8 al final. Un fallo no publica una
playlist completa. Pueden quedar segmentos parciales. Una clave compartida sigue
permitiendo escribir en prefijos de otras ejecuciones: los UID evitan colisiones
accidentales, no son aislamiento IAM entre workers.

## Software y advertencias comprobadas

Imágenes propias construidas desde commits oficiales fijados; ambos upstream
están archivados. Versiones y commits se comprobaron ejecutando --version:

| Imagen | Commit | Digest del índice importado |
|---|---|---|
| mansiflix/minio:source-9e49d5e7a648-amd64-r1 | 9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a | sha256:64deda952a6e27ad5ba649f5ce643c398fb28cdfb4a138a97eabb048b2ac7fc3 |
| mansiflix/minio-mc:source-7394ce0dd2a8-amd64-r1 | 7394ce0dd2a80935aded936b09fa12cbb3cb8096 | sha256:8596f69b617c0d50a9bfa1446b544019c187c272b48ba0a31bb16a784629a81f |
| mansiflix/video-worker:demo-88bb1d91e247 | imagen existente, retag exclusivo, sin reconstruir | sha256:88bb1d91e247cab6126993a6459ed7e2f1c775f98ba99ec4bd43b1e4d23d2bcf |

Docker Scout detectó 15 críticas/39 altas en el servidor y 10 críticas/21 altas
en el cliente el 27/10/2026. Son hallazgos de paquetes, no una evaluación completa
de explotabilidad. No se corrigieron estas vulnerabilidades. Entre los avisos
oficiales hay fallas de autenticación S3; algunas correcciones solo se publicaron
en AIStor. OIDC, LDAP, AMQP, SFTP y replicación no se configuran en esta demo,
pero esto no resuelve las fallas que sí afectan S3.

- [Avisos oficiales](https://github.com/minio/minio/security/advisories).
- [Bypass de firma en cargas](https://github.com/minio/minio/security/advisories/GHSA-hv4r-mvr4-25vw).
- [Bypass en Snowball](https://github.com/minio/minio/security/advisories/GHSA-9c4q-hq6p-c237).

Las pruebas negativas verifican operaciones normales de los clientes utilizados;
NO demuestran resistencia a estos exploits. La red restringida reduce exposición,
no corrige el servidor. No usar archivos reales ni ampliar acceso. Antes de un
uso posterior, resolver software mantenido, vulnerabilidades, TLS y controles de
acceso. No se instaló AIStor ni se cambió de producto.

`image/provenance.json` conserva bases, licencias, procedencia y verificaciones.
Las imágenes no se publicaron en un registry. imagePullPolicy Never requiere
importarlas en cada nodo elegible; el índice y sus capas deben conservarse.

## Persistencia, recursos y reinicios

PVC `mansiflix-minio-data`, 20 GiB, Filesystem, RWO, StorageClass
`csi-cinder-sc-retain`. Clase y PV nuevo verificaron Retain. Solo MinIO monta el
PVC. RWO no significa acceso multi-nodo; los workers usan S3 y emptyDir.

El Deployment tiene una réplica, Recreate, probes, solicitud 500m/1Gi y límite
1 CPU/2Gi. Una actualización o reinicio interrumpe el servicio. Recreate no
proporciona fencing frente a particiones. Los Jobs tienen recursos y plazos
acotados, UID/GID 10001, raíz de solo lectura, seccomp, capabilities eliminadas y
sin token del ServiceAccount. La capacidad de nodos y cuota se debe revisar antes
de nuevas cargas; las observaciones de una ejecución no reservan capacidad futura.

Retain no es respaldo. Ante problemas de Cinder, inspeccionar eventos del PVC y
Pod nuevos. No borrar claims ni forzar detach. Si falla el nodo, esperar la
recuperación o re-adjunción normal de CSI y escalar a administración.

## Secrets y permisos

Dos Secrets independientes: mansiflix-minio-admin y mansiflix-minio-worker.
Las variables sensibles solo usan secretKeyRef. No usar set -x, printenv,
volcados de Secrets, mc --debug ni credenciales en argumentos.

El administrador se entrega a MinIO y Jobs de bootstrap, seed y auditoría. El
Job HLS no lo referencia. Su encoder no recibe ninguna credencial. common.sh es
solo para herramientas administrativas, nunca un entrypoint de workers normales.

La política worker permite GetBucketLocation, lectura de originales, PutObject y
AbortMultipartUpload en encoded/runs/*. GetObject en encoded/runs/_checks/* se
reserva a la comparación sintética. No concede lectura de HLS procesado, listing,
borrado, administración ni escritura de originales. Las credenciales compartidas
no separan workers hostiles entre sí.

create-secrets.py exige contexto y confirmación; rehúsa nombres existentes, genera
en memoria, envía por stdin y no imprime ni guarda valores. Sus dos creaciones
no son atómicas: una falla parcial requiere inspección de nombres, sin rotación
ni rollback automático. No volver a ejecutarlo si los dos Secrets ya existen.

No se verificó cifrado de etcd ni la política externa de auditoría de Secrets.
Quien pueda crear Pods/Jobs o usar exec en herramientas administrativas puede
obtener sus credenciales. RBAC y el control del namespace dependen también de la
facultad. NetworkPolicy no sustituye esos controles.

## Red

Service ClusterIP minio:9000; consola desactivada; sin NodePort, LoadBalancer,
Ingress público ni túnel público. HTTP interno no cifra el tráfico.

minio-ingress permite solo TCP/9000 desde Pods del mismo namespace con ambas:
app.kubernetes.io/part-of=mansiflix y mansiflix.local/minio-access=true.
No restringe egress; otras NetworkPolicies son aditivas y las etiquetas pueden
ser copiadas por alguien autorizado a crear workloads. Calico está presente;
el enforcement debe comprobarse con pruebas, no inferirse de la existencia YAML.

## Validación y ejecución reproducible

Desde la raíz del repositorio, kubectl kustomize deploy/minio renderiza cinco
recursos base. Los Jobs son separados y no arrancan por renderizar la base.

```bash
kubectl kustomize deploy/minio
# Usar Go >=1.26 instalado; no contacta al API server:
(cd video-operator && go run ../deploy/minio/scripts/validate-local.go ../deploy/minio)
```

No aplicar ciegamente la base sobre recursos existentes: revisar nombres y diff.
La primera instalación creó el PVC por separado para comprobar Cinder, luego
ConfigMap/Service/Deployment/NetworkPolicy ausentes. create falla ante colisión;
no usar prune, force, borrado del PVC ni aplicar recursos del almacenamiento viejo.

Los comandos de la ejecución y resultados detallados se conservan fuera de Git
en el directorio de evidencia indicado durante la demo. No contienen valores de
Secrets. Para repetir desde una laptop con SSH al control-plane:

```bash
: "${MANSIFLIX_SSH_TARGET:?Definir el destino SSH autorizado}"
: "${MINIO_EVIDENCE_DIR:?Definir directorio privado para evidencia}"
# Default fue el contexto comprobado; confirmar que siga siendo el correcto.
ssh "$MANSIFLIX_SSH_TARGET" sudo -n k0s kubectl config current-context

python3 deploy/minio/scripts/run-job.py smoke-test-job.yaml \
  --context Default --ssh-target "$MANSIFLIX_SSH_TARGET" \
  --evidence-dir "$MINIO_EVIDENCE_DIR" --confirm-create-job

python3 deploy/minio/scripts/run-job.py hls-demo-job.yaml \
  --context Default --ssh-target "$MANSIFLIX_SSH_TARGET" \
  --evidence-dir "$MINIO_EVIDENCE_DIR" --confirm-create-job
```

run-job.py valida un recurso nuevo, guarda/revisa un diff de solo creación y
rehúsa nombres existentes. Cada ejecución tiene Job y prefijo distintos; no
recrea Deployment, PVC, Secrets ni buckets. Guarda UID, estado, logs y comandos.
Desde el nodo se puede omitir --ssh-target si kubectl está disponible como
wrapper de `sudo -n k0s kubectl`; no cambiar la configuración global para crearlo.

Bootstrap es idempotente dentro de esta instalación: conserva credenciales,
reconcilia buckets/policy y valida el Secret worker mediante una lectura real.
No es una herramienta para adoptar un MinIO ajeno. seed-demo-job.yaml crea un
MP4 de 8 segundos y lo carga en demo/synthetic.mp4; repetirlo reemplaza solo ese
objeto sintético. No es necesario repetir seed para cada prueba HLS.

verify-demo-job.yaml necesita parámetros explícitos mediante --env:

- TEST_MODE=hls y CHECK_UID=<UID del Pod HLS>: descarga playlist y segmentos,
  valida referencias y compara SHA-256 con el manifiesto subido.
- TEST_MODE=persistence, CHECK_UID=<UID del Pod smoke> y EXPECTED_SHA256=<hash
  registrado antes del reinicio>: descarga el MISMO objeto tras el reinicio.
- TEST_MODE=cleanup y CHECK_UID=<UID smoke>: elimina únicamente sus tres claves
  sintéticas exactas. Nunca hace borrado recursivo de buckets.

network-test-job.yaml admite --network-peer allowed, no-labels, part-only o
access-only. Comprueba DNS por separado. Un 403 no acredita bloqueo de red y un
fallo DNS tampoco. Usar control positivo antes/después y nodos comprobados.
Las pruebas iniciales de nodo-3 fallaron; no se corrigió la red global del clúster.

Limpiar Jobs solo por nombre y UID de esta ejecución, después de guardar logs.
Nunca seleccionar todos los Jobs del namespace. Conservar la evidencia y los
artefactos HLS de demostración; los originales reales siguen fuera del alcance.

## Respaldo y recuperación

No hay backup automático ni RPO/RTO garantizados. Para un respaldo futuro, elegir
un destino privado independiente del PVC/Cinder y copiar por S3 únicamente
objetos cerrados, sin flags destructivos. No montar/copiar directamente /data.
Un mirror durante escrituras no es una snapshot consistente.

```bash
: "${BACKUP_DIR:?Destino independiente, protegido y NUEVO}"
[ ! -e "$BACKUP_DIR" ] || { printf 'El destino ya existe\n' >&2; exit 1; }
umask 077
mkdir -p "$BACKUP_DIR"
# En cliente autorizado con alias administrativo configurado en privado:
mc mirror admin/mansiflix-originals "$BACKUP_DIR/originals"
mc mirror admin/mansiflix-encoded "$BACKUP_DIR/encoded"
```

Conservar inventario y hashes; respaldar Secrets mediante un mecanismo privado
separado. No poner sus valores en comandos ni archivos versionados. Esta copia
no preserva necesariamente versiones, políticas, ACL, etiquetas o metadatos S3.

Un reinicio normal reutiliza el PVC. Ante pérdida del disco, recuperar en OTRO
volumen nuevo y restaurar por S3, verificando inventario y hashes antes de usarlo.
Si se elimina accidentalmente un PVC Retain, revisar el PV liberado con la
facultad; no automatizar claimRef ni borrar discos. Sin copia verificada no se
puede prometer recuperación. No se ejecutó una restauración de backup en esta demo.
