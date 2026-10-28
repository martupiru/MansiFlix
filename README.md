# MansiFlix 

## Arquitectura
                  

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


                   FLUJO DE PROCESAMIENTO

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


                   OBSERVABILIDAD
                Prometheus + Grafana
