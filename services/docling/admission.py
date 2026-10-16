"""Bounded admission for the pinned Docling Serve RQ engine.

Slots follow RQ job state, never the lifetime of a caller's HTTP connection.
Job enqueue and admission membership commit in the same Redis transaction.
"""
import os

from docling_jobkit.orchestrators.base_orchestrator import RedisBackpressureError
from redis.exceptions import LockError
from rq import Queue
from rq.job import JobStatus


class BoundedQueue(Queue):
    def enqueue_call(self, *args, **kwargs):
        capacity = int(os.environ.get("DOCUMENT_QUEUE_CAPACITY", "16"))
        ttl = int(os.environ.get("DOCUMENT_QUEUE_WAIT_SECONDS", "120"))
        if not 1 <= capacity <= 1024 or not 1 <= ttl <= 600:
            raise ValueError("invalid document queue bounds")
        if kwargs.get("pipeline") is not None:
            raise ValueError("external enqueue pipelines are not supported")
        key = f"gateway:docling:admission:{self.name}"
        try:
            with self.connection.lock(key + ":lock", timeout=30, blocking_timeout=0.1):
                # Fail closed when Redis is unavailable. Do not discard slots
                # on a lease deadline while their jobs could still execute.
                task_ids = list(self.connection.smembers(key))
                with self.connection.pipeline(transaction=False) as pipe:
                    for task_id in task_ids:
                        pipe.hget(b"rq:job:" + task_id, "status")
                    statuses = pipe.execute()
                terminal = {s.value.encode() for s in (JobStatus.FINISHED, JobStatus.FAILED, JobStatus.STOPPED, JobStatus.CANCELED)}
                for task_id, status in zip(task_ids, statuses):
                    if status is None or status in terminal:
                        self.connection.srem(key, task_id)
                        if status is None:
                            self.connection.lrem(self.key, 0, task_id)
                if self.connection.scard(key) >= capacity:
                    raise RedisBackpressureError("document queue is full")
                with self.connection.pipeline(transaction=True) as pipe:
                    kwargs["description"] = "document conversion"
                    kwargs["pipeline"] = pipe
                    kwargs["ttl"] = ttl
                    job = super().enqueue_call(*args, **kwargs)
                    pipe.sadd(key, job.id)
                    pipe.execute()
                return job
        except LockError as exc:
            raise RedisBackpressureError("document admission busy") from exc
