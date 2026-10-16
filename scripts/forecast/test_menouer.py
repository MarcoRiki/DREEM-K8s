"""
Tests of the decision rule (menouer.py), without a cluster: python -m unittest test_menouer
"""
import unittest

from menouer import SCALE_DOWN, SCALE_UP, NO_ACTION, ClusterState, Params, decide

PAPER = Params()  # Load_up 70%, Load_down 40%, Pending_pod 2 minutes, every minute


def state(cpu=0.0, memory=0.0, storage=0.0, active=3, powered_off=1, pending=()):
    return ClusterState(load_cpu=cpu, load_memory=memory, load_storage=storage,
                        active_nodes=active, pending_nodes=powered_off, pending_pod_ages=list(pending))


class PaperExampleTest(unittest.TestCase):
    """
    Section 4 of the FRAME'24 paper: two identical nodes of 8 CPUs, node1 active and
    node2 stopped; two pods of 6 CPUs that run for 120s, Pod1 launched at T0 and Pod2 at
    T0+60. Only the CPU criterion is considered. node2 is added at T0+60 and removed at
    T0+120, when Pod1 stops. In the paper node1 is the master, which also runs pods and
    is never stopped: here it is a worker and the floor is one node.
    """

    def test_node_added_at_t0_plus_60_and_removed_at_t0_plus_120(self):
        timeline = [
            # T0: Pod1 on node1, 6*100/8 = 75%, nothing pending
            ("T0", state(cpu=75.0, active=1, powered_off=1), NO_ACTION),
            # T0+60: Pod2 cannot run on node1 (2 CPUs free): pending, load 75% >= 70%
            ("T0+60", state(cpu=75.0, active=1, powered_off=1, pending=[0.0]), SCALE_UP),
            # T0+120: Pod1 stops, Pod2 runs on node2: 6*100/16 = 37.5% <= 40%
            ("T0+120", state(cpu=37.5, active=2, powered_off=0), SCALE_DOWN),
        ]
        for when, cluster, expected in timeline:
            with self.subTest(when=when):
                self.assertEqual(decide(cluster, PAPER, min_nodes=1, max_nodes=2).action, expected)


class ScaleUpTest(unittest.TestCase):

    def test_any_load_at_load_up_with_a_pending_pod(self):
        for load in ({"cpu": 70.0}, {"memory": 70.0}, {"storage": 85.0}):
            with self.subTest(load=load):
                decision = decide(state(pending=[5.0], **load), PAPER, 3, 8)
                self.assertEqual((decision.action, decision.rule), (SCALE_UP, "load-up"))

    def test_high_load_alone_does_not_scale_up(self):
        self.assertEqual(decide(state(cpu=95.0), PAPER, 3, 8).action, NO_ACTION)

    def test_pod_pending_for_more_than_pending_pod(self):
        self.assertEqual(decide(state(cpu=20.0, pending=[120.0]), PAPER, 3, 8).action, NO_ACTION)
        decision = decide(state(cpu=20.0, pending=[30.0, 121.0]), PAPER, 3, 8)
        self.assertEqual((decision.action, decision.rule), (SCALE_UP, "pending-pod"))

    def test_needs_a_powered_off_node(self):
        cluster = state(cpu=90.0, active=8, powered_off=0, pending=[300.0])
        self.assertEqual(decide(cluster, PAPER, 3, 8).action, NO_ACTION)

    def test_stops_at_max_nodes(self):
        cluster = state(cpu=90.0, active=6, powered_off=2, pending=[300.0])
        self.assertEqual(decide(cluster, PAPER, 3, 6).action, NO_ACTION)
        self.assertEqual(decide(cluster, PAPER, 3, 7).action, SCALE_UP)


class ScaleDownTest(unittest.TestCase):

    def test_every_load_at_most_load_down(self):
        decision = decide(state(cpu=40.0, memory=12.0, storage=30.0, active=5), PAPER, 3, 8)
        self.assertEqual((decision.action, decision.rule), (SCALE_DOWN, "load-down"))

    def test_one_load_above_load_down_blocks_it(self):
        for load in ({"cpu": 40.5}, {"memory": 41.0}, {"storage": 55.0}):
            with self.subTest(load=load):
                self.assertEqual(decide(state(active=5, **load), PAPER, 3, 8).action, NO_ACTION)

    def test_a_pending_pod_blocks_it(self):
        self.assertEqual(decide(state(cpu=10.0, active=5, pending=[10.0]), PAPER, 3, 8).action, NO_ACTION)

    def test_stops_at_min_nodes(self):
        self.assertEqual(decide(state(cpu=5.0, active=3), PAPER, 3, 8).action, NO_ACTION)
        self.assertEqual(decide(state(cpu=5.0, active=4), PAPER, 3, 8).action, SCALE_DOWN)


if __name__ == "__main__":
    unittest.main()
