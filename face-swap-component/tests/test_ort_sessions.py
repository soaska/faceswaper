from types import SimpleNamespace

from ort_sessions import configured_insightface_sessions, create_session_options


class FakeSession:
    def __init__(self, model_path, **kwargs):
        self.model_path = model_path
        self.kwargs = kwargs


def test_session_options_disable_automatic_affinity():
    class Options:
        pass

    fake_ort = SimpleNamespace(
        SessionOptions=Options,
        ExecutionMode=SimpleNamespace(ORT_SEQUENTIAL="sequential"),
    )

    options = create_session_options(fake_ort)

    assert options.intra_op_num_threads == 1
    assert options.inter_op_num_threads == 1
    assert options.execution_mode == "sequential"


def test_insightface_sessions_receive_options_and_original_is_restored():
    module = SimpleNamespace(PickableInferenceSession=FakeSession)
    options = object()

    with configured_insightface_sessions(module, options):
        session = module.PickableInferenceSession("model.onnx", providers=["CUDA"])
        assert session.kwargs["sess_options"] is options
        assert session.kwargs["providers"] == ["CUDA"]

    assert module.PickableInferenceSession is FakeSession
