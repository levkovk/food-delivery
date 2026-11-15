FROM nginxinc/nginx-unprivileged:1.27-alpine
ENV GATEWAY_URL=http://gateway:8080
COPY templates/ /etc/nginx/templates/
COPY static/ /usr/share/nginx/html/
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
