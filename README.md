# MansiFlix 

## Arquitectura
                  

                        OPENSTACK
                           │
                           ▼
                       KUBERNETES
                           │
                           ▼
                  OpenStack LoadBalancer
                           │
                           ▼
                     Gateway API
                           │
                           ▼
                    Envoy Gateway
                 ┌─────────┼─────────┐
                 ▼         ▼         ▼
             Frontend    API Go   Video Service
          React/TS/Vite    │          │
                           │          │
                           │          └── sirve HLS
                           │
                           ├── PostgreSQL
                           │
                           ├── Cinder PVC
                           │     originals/
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
             Job 360    Job 720    Job 1080
                │          │          │
            Go+FFmpeg  Go+FFmpeg  Go+FFmpeg
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
                     Video Service
                           │
                           ▼
                     Envoy Gateway
                           │
                           ▼
                        Cliente


               Prometheus + Grafana
