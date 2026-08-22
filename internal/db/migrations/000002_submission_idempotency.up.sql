ALTER TABLE public.benchmark_result
    ADD COLUMN submission_key text,
    ADD COLUMN submission_payload_sha256 text;

ALTER TABLE public.benchmark_result
    ADD CONSTRAINT benchmark_result_submission_idempotency_check
    CHECK (
        (submission_key IS NULL AND submission_payload_sha256 IS NULL)
        OR (
            submission_key IS NOT NULL
            AND submission_payload_sha256 IS NOT NULL
            AND submission_payload_sha256 ~ '^[0-9a-f]{64}$'
        )
    ) NOT VALID;

CREATE UNIQUE INDEX CONCURRENTLY benchmark_result_submission_key_index
    ON public.benchmark_result (submission_key)
    WHERE submission_key IS NOT NULL;

ALTER TABLE public.benchmark_result
    VALIDATE CONSTRAINT benchmark_result_submission_idempotency_check;
