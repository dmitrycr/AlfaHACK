#!/bin/sh
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  --set=auth_password="$AUTH_DB_PASSWORD" --set=company_password="$COMPANY_DB_PASSWORD" \
  --set=news_password="$NEWS_DB_PASSWORD" --set=events_password="$EVENTS_DB_PASSWORD" \
  --set=analytics_password="$ANALYTICS_DB_PASSWORD" <<'SQL'
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE ROLE app_auth LOGIN PASSWORD :'auth_password';
CREATE ROLE app_company LOGIN PASSWORD :'company_password';
CREATE ROLE app_news LOGIN PASSWORD :'news_password';
CREATE ROLE app_events LOGIN PASSWORD :'events_password';
CREATE ROLE app_analytics LOGIN PASSWORD :'analytics_password';
CREATE SCHEMA auth AUTHORIZATION app_auth;
CREATE SCHEMA company AUTHORIZATION app_company;
CREATE SCHEMA news AUTHORIZATION app_news;
CREATE SCHEMA events AUTHORIZATION app_events;
CREATE SCHEMA analytics AUTHORIZATION app_analytics;
REVOKE ALL ON SCHEMA auth,company,news,events,analytics FROM PUBLIC;
SQL
