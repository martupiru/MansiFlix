# MansiFlix 

## 1. Arquitectura General

```text
             OPENSTACK
                            │
             ┌──────────────┴──────────────┐
             │                             │
       Load Balancer                 Cinder CSI
             │                             │
             ▼                             ▼
        KUBERNETES                 Cinder Volume nuevo
             │                             │
             ▼                             ▼
        Gateway API                  PVC MinIO (RWO)
             │                             │
             ▼                             │
        Envoy Gateway                      │
      ┌──────┼──────────┐                  │
      ▼      ▼          ▼                  ▼
  Frontend  API Go   Video Service      MinIO
 React/TS    │          │            API S3 interna
  /Vite      │          │                  │
             │          │                  │
             │          │                  │
             ▼          │                  │
        PostgreSQL      │                  │
      (metadatos)       │                  │
                        │                  │
                        └─────────┬────────┘
                                  │
                          Lectura de HLS
                          mediante S3
                                  │
                                  ▼
                           Cliente reproduce
                            HLS (.m3u8)
                          + segmentos (.ts)
```

**Notas aclaratorias:**
* **Ingreso de Tráfico:** Todo el tráfico externo pasa a través de Gateway API (implementado con Envoy Gateway), balanceado desde OpenStack.
* **Almacenamiento Persistente:** MinIO opera sobre volúmenes aprovisionados dinámicamente mediante Cinder CSI.
* **Reproducción:** El *Video Service* sirve como intermediario para que el cliente pueda consumir los archivos HLS directamente desde el bucket de MinIO.

---

## 2. Flujo de Procesamiento

```text
       API Go / solicitud de procesamiento
                       │
                       ▼
                Kubernetes API
                       │
                       ▼
                VideoEncoding CRD
                       │
                       ▼
                  Operator Go
             ┌─────────┼─────────┐
             ▼         ▼         ▼
          Job 360    Job 720   Job 1080
             │         │         │
             ▼         ▼         ▼
          Worker    Worker    Worker
         Go+FFmpeg Go+FFmpeg Go+FFmpeg
             │         │         │
             └─────────┼─────────┘
                       │
             1. Descarga original por S3
                       │
                       ▼
               Procesamiento local
                  con FFmpeg
                       │
                       ▼
             Generación de HLS
              .m3u8 + segmentos
                       │
                       ▼
             Subida de resultados por S3
                       │
                       ▼
                     MinIO
             ┌─────────┴──────────┐
             │                    │
        originals/             encoded/
       Videos originales      videos/runs/
                              <ejecución>/
                                  │
                                  ▼
                            Video Service
                                  │
                                  ▼
                            Envoy Gateway
                                  │
                                  ▼
                                Cliente
```

**Notas aclaratorias:**
* **Orquestación (Patrón Operator):** Un Custom Resource (`VideoEncoding`) dispara el Operator escrito en Go, encargándose de lanzar trabajos de codificación nativos en Kubernetes (`Jobs`).
* **Concurrencia:** Los workers procesan las distintas resoluciones (360p, 720p, 1080p) en paralelo utilizando FFmpeg.
* **Output HLS:** Cada worker descarga el video original, lo transcodifica a formato HLS y sube el manifiesto (`.m3u8`) junto con los fragmentos de video (`.ts`) a rutas específicas dentro del bucket `encoded`.

---

## 3. Observabilidad

* **Stack utilizado:** Prometheus + Grafana para el monitoreo de métricas, consumo de recursos del clúster y tiempos de procesamiento de los workers.
