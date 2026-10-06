"""Run the upstream worker with metadata-safe RQ logging."""
from docling_serve.rq_worker_instrumented import InstrumentedRQWorker

_original_work = InstrumentedRQWorker.work


def quiet_work(self, *args, **kwargs):
    # Upstream CLI calls work() without a level; RQ otherwise resets it to INFO.
    kwargs["logging_level"] = "WARNING"
    return _original_work(self, *args, **kwargs)


InstrumentedRQWorker.work = quiet_work

from docling_serve.__main__ import rq_worker  # noqa: E402

if __name__ == "__main__":
    rq_worker()
