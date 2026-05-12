# Kubernetes Patterns for Valkey/Redis

## Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: valkey
spec:
  replicas: 1
  selector:
    matchLabels:
      app: valkey
  template:
    metadata:
      labels:
        app: valkey
    spec:
      containers:
        - name: valkey
          image: docker.io/valkey/valkey:8-alpine
          ports:
            - containerPort: 6379
          readinessProbe:
            exec:
              command: ["redis-cli", "ping"]
            initialDelaySeconds: 5
            periodSeconds: 5
          livenessProbe:
            exec:
              command: ["redis-cli", "ping"]
            initialDelaySeconds: 15
            periodSeconds: 10
          resources:
            requests:
              memory: "128Mi"
              cpu: "100m"
            limits:
              memory: "256Mi"
```

## Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: valkey
spec:
  type: ClusterIP
  selector:
    app: valkey
  ports:
    - port: 6379
      targetPort: 6379
```

## Persistence (PVC)

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: valkey-data
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 1Gi
```

Mount at `/data` in the container spec:

```yaml
volumeMounts:
  - name: data
    mountPath: /data
volumes:
  - name: data
    persistentVolumeClaim:
      claimName: valkey-data
```
