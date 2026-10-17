#!/usr/bin/env bash
set -uo pipefail
export DEBIAN_FRONTEND=noninteractive

if ! command -v psql >/dev/null 2>&1; then
  apt-get update -qq >/tmp/devdb-apt.log 2>&1
  apt-get install -y -qq postgresql-16 postgresql-client-16 redis-server >>/tmp/devdb-apt.log 2>&1
fi

if ! redis-cli ping >/dev/null 2>&1; then
  redis-server --daemonize yes --save "" >/tmp/devdb-redis.log 2>&1 || true
fi

if ! pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1; then
  service postgresql start >/tmp/devdb-pg.log 2>&1 || pg_ctlcluster 16 main start >>/tmp/devdb-pg.log 2>&1 || true
  for _ in $(seq 1 20); do pg_isready -h 127.0.0.1 -p 5432 >/dev/null 2>&1 && break; sleep 0.5; done
fi

su postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='guanjin'\"" 2>/dev/null | grep -q 1 \
  || su postgres -c "psql -c \"CREATE ROLE guanjin LOGIN PASSWORD 'guanjin' SUPERUSER;\"" >/dev/null 2>&1
su postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='guanjin'\"" 2>/dev/null | grep -q 1 \
  || su postgres -c "createdb -O guanjin guanjin" >/dev/null 2>&1

echo "DATABASE_URL=postgres://guanjin:guanjin@127.0.0.1:5432/guanjin?sslmode=disable"
echo "REDIS_URL=redis://127.0.0.1:6379/0"
pg_isready -h 127.0.0.1 -p 5432 && redis-cli ping