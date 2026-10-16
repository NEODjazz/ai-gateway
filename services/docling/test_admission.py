"""Integration contracts against an isolated real Redis queue."""
import os
import unittest
import uuid
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import patch

import redis
from docling_jobkit.orchestrators.base_orchestrator import RedisBackpressureError
from rq.job import JobStatus

from admission import BoundedQueue


class AdmissionIntegration(unittest.TestCase):
    def setUp(self):
        self.conn = redis.Redis.from_url(os.environ["DOCLING_SERVE_ENG_RQ_REDIS_URL"])
        self.name = "document-test-" + uuid.uuid4().hex
        self.queue = BoundedQueue(self.name, connection=self.conn)
        self.key = "gateway:docling:admission:" + self.name
        self.jobs = []
        self.env = patch.dict(os.environ, DOCUMENT_QUEUE_CAPACITY="2", DOCUMENT_QUEUE_WAIT_SECONDS="120")
        self.env.start()

    def tearDown(self):
        for job in self.jobs:
            if self.conn.exists(job.key):
                job.delete()
        self.queue.delete(delete_jobs=True)
        self.conn.delete(self.key, self.key + ":lock")
        self.env.stop()
        self.conn.close()

    def enqueue(self):
        job = self.queue.enqueue("builtins.sum", args=([1, 2],))
        self.jobs.append(job)
        return job

    def test_pending_and_started_jobs_hold_capacity(self):
        first = self.enqueue()
        first.set_status(JobStatus.STARTED)
        self.enqueue()
        with self.assertRaises(RedisBackpressureError):
            self.enqueue()
        # A client disconnect does not change RQ state or release admission.
        self.assertEqual(self.conn.scard(self.key), 2)
        first.set_status(JobStatus.FINISHED)
        replacement = self.enqueue()
        self.assertEqual(replacement.ttl, 120)
        self.assertEqual(self.conn.scard(self.key), 2)

    def test_missing_job_does_not_leave_dead_queue_id(self):
        expired = self.enqueue()
        self.enqueue()
        self.conn.delete(expired.key)
        self.enqueue()
        self.assertNotIn(expired.id, self.queue.job_ids)
        self.assertEqual(self.conn.scard(self.key), 2)

    def test_concurrent_api_instances_cannot_exceed_limit(self):
        def attempt(_):
            queue = BoundedQueue(self.name, connection=self.conn)
            try:
                return queue.enqueue("builtins.sum", args=([1, 2],))
            except RedisBackpressureError:
                return None
        with ThreadPoolExecutor(max_workers=12) as pool:
            jobs = [j for j in pool.map(attempt, range(24)) if j is not None]
        self.jobs.extend(jobs)
        self.assertGreaterEqual(len(jobs), 1)
        self.assertLessEqual(len(jobs), 2)
        while len(self.jobs) < 2:
            self.enqueue()
        self.assertEqual(self.conn.scard(self.key), 2)
        self.assertEqual(self.queue.count, 2)

    def test_unavailable_redis_never_admits_job(self):
        conn = redis.Redis(host="127.0.0.1", port=1, socket_connect_timeout=0.1, socket_timeout=0.1)
        self.addCleanup(conn.close)
        with self.assertRaises(redis.exceptions.RedisError):
            BoundedQueue(self.name, connection=conn).enqueue("builtins.sum", args=([1, 2],))


if __name__ == "__main__":
    unittest.main()
