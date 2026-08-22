DROP INDEX CONCURRENTLY public.benchmark_result_submission_key_index;

ALTER TABLE public.benchmark_result
    DROP CONSTRAINT benchmark_result_submission_idempotency_check,
    DROP COLUMN submission_payload_sha256,
    DROP COLUMN submission_key;
