# MansiFlix 

## Arquitectura
                    
                    OPENSTACK
                        │
                        ▼
                 KUBERNETES
                        │
          ┌─────────────┼─────────────┐
          ▼             ▼             ▼
       Frontend        API        PostgreSQL
    React/TS/Vite       Go
                        │
                 Kubernetes API
                        │
                        ▼
                 VideoEncoding
                     (CRD)
                        │
                        ▼
                  Operator Go
                        │
             ┌──────────┼──────────┐
             ▼          ▼          ▼
          Job 360     Job 720    Job 1080
             │          │          │
             └──────────┼──────────┘
                        ▼
                      FFmpeg
                        │
                        ▼
                 Cinder + PVC
                        │
                        ▼
                    Videos
                        │
                        ▼
                HLS (HTTP Live Streaming)


El administrador sube un video → la API lo guarda → crea un VideoEncoding → el Operator detecta ese recurso → crea Jobs de Kubernetes → FFmpeg genera las distintas resoluciones → se almacenan en el PVC → finalmente el usuario puede reproducir el video.
