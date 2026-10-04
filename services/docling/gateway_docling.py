"""Internal-only Docling API; conversion and workers remain upstream code."""
import asyncio
import os
from contextlib import asynccontextmanager, suppress
from redis.exceptions import RedisError

from admission import BoundedQueue
from docling_jobkit.orchestrators.rq.orchestrator import RQOrchestrator

if os.environ.get("DOCLING_SERVE_ENG_KIND") != "rq" or not os.environ.get("DOCLING_SERVE_API_KEY"):
    raise RuntimeError("Docling requires RQ and an internal API key")

_original_queue_factory = RQOrchestrator.make_rq_queue


def make_bounded_queue(config):
    conn, queue = _original_queue_factory(config)
    return conn, BoundedQueue(
        queue.name, connection=conn, default_timeout=config.job_timeout,
        result_ttl=config.results_ttl, failure_ttl=config.failure_ttl,
    )


RQOrchestrator.make_rq_queue = staticmethod(make_bounded_queue)

from docling_serve.app import create_app  # noqa: E402
from docling_serve.orchestrator_factory import get_async_orchestrator  # noqa: E402

app = create_app()
_original_lifespan = app.router.lifespan_context


async def prune_terminal_cache():
    while True:
        await asyncio.sleep(1)
        orchestrator = get_async_orchestrator()
        # Redis is the authoritative store. Discard completed task payloads
        # rather than retaining uploaded documents forever in API memory.
        snapshot = list(orchestrator.tasks.items())
        try:
            async with orchestrator._async_redis_conn.pipeline(transaction=False) as pipe:
                for task_id, _ in snapshot:
                    pipe.hget("rq:job:" + task_id, "status")
                statuses = await pipe.execute()
        except RedisError:
            continue
        for (task_id, task), status in zip(snapshot, statuses):
            if task.is_completed() or status in (None, b"finished", b"failed", b"stopped", b"canceled"):
                orchestrator.tasks.pop(task_id, None)
        # RQ results use a deterministic Redis key and can be reloaded.
        orchestrator._task_result_keys.clear()


@asynccontextmanager
async def bounded_lifespan(application):
    async with _original_lifespan(application):
        cleanup = asyncio.create_task(prune_terminal_cache())
        try:
            yield
        finally:
            cleanup.cancel()
            with suppress(asyncio.CancelledError):
                await cleanup


app.router.lifespan_context = bounded_lifespan

from request_limits import RequestLimits  # noqa: E402
app.add_middleware(RequestLimits)
