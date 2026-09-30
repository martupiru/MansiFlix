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


## Arquitectura opción 2

                        OPENSTACK
                           │
                           ▼
                       KUBERNETES
                           │
                    Load Balancer
                           │
                           ▼
                        Ingress
                     ┌─────┴─────┐
                     ▼           ▼
                 Frontend      API Go
              React/TS/Vite      │
                                 ├──── PostgreSQL
                                 │
                                 ├──── Cinder PVC
                                 │      originals/
                                 │
                                 ▼
                           Kubernetes API
                                 │
                                 ▼
                         VideoEncoding CRD
                                 │
                                 ▼
                           Operator Go
                      ┌──────────┼──────────┐
                      ▼          ▼          ▼
                  Job 360     Job 720    Job 1080
                      │          │          │
                  Go+FFmpeg   Go+FFmpeg   Go+FFmpeg
                      │          │          │
                      └──────────┼──────────┘
                                 ▼
                           Cinder + PVC
                          encoded/videos
                                 │
                                 ▼
                         HLS .m3u8 + segments
                                 │
                                 ▼
                           Nginx Video
                              Server
                                 │
                                 ▼
                              Cliente


                   Prometheus + Grafana
                            │
                  monitorea todo el cluster
