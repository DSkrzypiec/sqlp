CREATE MODEL zm OPTIONS (model_type = 'LINEAR_REG', input_label_cols = ['a']) AS SELECT a, b FROM t
CREATE OR REPLACE MODEL zm OPTIONS (model_type = 'LINEAR_REG') AS SELECT a AS label, b FROM t
CREATE MODEL IF NOT EXISTS zm OPTIONS (model_type = 'LINEAR_REG') AS SELECT a AS label, b FROM t
CREATE OR REPLACE MODEL IF NOT EXISTS zm OPTIONS (model_type = 'LINEAR_REG') AS SELECT a AS label FROM t
CREATE TEMP MODEL zm OPTIONS (model_type = 'LINEAR_REG') AS SELECT a AS label, b FROM t
CREATE MODEL zm TRANSFORM (a * 2 AS a2, b) OPTIONS (model_type = 'LINEAR_REG') AS SELECT a AS label, b FROM t
CREATE MODEL zm TRANSFORM (* EXCEPT (a), ML.STANDARD_SCALER(b) OVER () AS bs) OPTIONS (model_type = 'LINEAR_REG') AS SELECT a AS label, b FROM t
CREATE MODEL zm INPUT (a INT64, b STRING) OUTPUT (c FLOAT64) REMOTE WITH CONNECTION `us.c` OPTIONS (endpoint = 'https://x')
CREATE MODEL zm REMOTE WITH CONNECTION DEFAULT OPTIONS (endpoint = 'gemini-2.0-flash')
CREATE MODEL zm REMOTE OPTIONS (endpoint = 'gemini-2.0-flash')
CREATE MODEL zm OPTIONS (model_type = 'TENSORFLOW', model_path = 'gs://b/m/*')
CREATE MODEL zm OPTIONS (model_type = 'ARIMA_PLUS', time_series_timestamp_col = 'd', time_series_data_col = 'a') AS (training_data AS (SELECT d, a FROM t), custom_holiday AS (SELECT 'US' AS region))
CREATE MODEL zm OPTIONS (model_type = 'LINEAR_REG') AS (SELECT a AS label FROM t)
CREATE MODEL zm AS SELECT a AS label FROM t
CREATE MODEL zm
CREATE MODEL zm OPTIONS (model_type = 'LINEAR_REG') TRANSFORM (a) AS SELECT a AS label FROM t
CREATE MODEL zm TRANSFORM () OPTIONS (model_type = 'LINEAR_REG') AS SELECT a AS label FROM t
CREATE MODEL zm INPUT (a INT64) OPTIONS (model_type = 'LINEAR_REG') AS SELECT a AS label FROM t
ALTER MODEL zm SET OPTIONS (description = 'x')
ALTER MODEL IF EXISTS zm SET OPTIONS (description = 'x')
ALTER MODEL zm ADD COLUMN a INT64
EXPORT MODEL zm OPTIONS (uri = 'gs://b/m')
EXPORT MODEL zm
EXPORT MODEL zm WITH CONNECTION `us.c` OPTIONS (uri = 'gs://b/m')
EXPORT DATA OPTIONS (uri = 'gs://b/x-*.csv', format = 'CSV') AS SELECT a FROM t
EXPORT DATA WITH CONNECTION `us.c` OPTIONS (uri = 's3://b/x-*.csv', format = 'CSV') AS SELECT a FROM t
EXPORT DATA OPTIONS (uri = 'gs://b/x-*.csv', format = 'CSV') AS (SELECT a FROM t)
EXPORT DATA OPTIONS (uri = 'gs://b/x-*.csv', format = 'CSV') SELECT a FROM t
EXPORT DATA AS SELECT a FROM t
EXPORT DATA OPTIONS (uri = 'gs://b/x-*.csv', format = 'CSV') AS WITH q AS (SELECT a FROM t) SELECT * FROM q
EXPORT DATA OPTIONS (uri = 'gs://b/x-*.csv', format = 'CSV') AS FROM t |> SELECT a
LOAD DATA INTO zl FROM FILES (format = 'CSV', uris = ['gs://b/x.csv'])
LOAD DATA OVERWRITE zl (a INT64, b STRING NOT NULL OPTIONS (description = 'x')) PARTITION BY a CLUSTER BY b OPTIONS (description = 'd') FROM FILES (format = 'CSV', uris = ['gs://b/x.csv']) WITH PARTITION COLUMNS (p STRING) WITH CONNECTION `us.c`
LOAD DATA INTO TEMP TABLE zl FROM FILES (format = 'CSV', uris = ['gs://b/x.csv'])
LOAD DATA INTO TEMPORARY TABLE zl (a INT64) FROM FILES (format = 'CSV', uris = ['gs://b/x.csv'])
LOAD DATA INTO zl PARTITIONS (d = DATE '2020-01-01') FROM FILES (format = 'CSV', uris = ['gs://b/x.csv'])
LOAD DATA INTO zl OVERWRITE PARTITIONS (d = DATE '2020-01-01') FROM FILES (format = 'CSV', uris = ['gs://b/x.csv'])
LOAD DATA zl FROM FILES (format = 'CSV', uris = ['gs://b/x.csv'])
LOAD DATA INTO zl FROM FILES ()
LOAD DATA INTO zl
LOAD DATA INTO zl FROM FILES (format = 'CSV', uris = ['gs://b/x.csv']) WITH CONNECTION `us.c` WITH PARTITION COLUMNS
LOAD DATA INTO zl WITH PARTITION COLUMNS FROM FILES (format = 'CSV', uris = ['gs://b/x.csv'])
LOAD DATA INTO zl OPTIONS () PARTITION BY a FROM FILES (format = 'CSV', uris = ['gs://b/x.csv'])
LOAD DATA INTO zl FROM FILES (format = 'CSV', uris = ['gs://b/x.csv']) WITH PARTITION COLUMNS
