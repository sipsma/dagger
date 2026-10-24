# wcp: regression checks for gap.py and slotsim.py. Run: python3 -I test_tools.py
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import gap  # noqa: E402
import slotsim  # noqa: E402


class OccupancyTest(unittest.TestCase):
    def test_query_starting_before_first_event(self):
        occ = gap.Occupancy([[10, 20]], names=("busy",))
        self.assertEqual(occ.integral(0, 30), [10.0])

    def test_partial_overlap_and_two_phases(self):
        occ = gap.Occupancy([[0, 10, 30], [5, 15, 20]], names=("a", "b"))
        # a: [0,10] and [5,15]; b: [10,30] and [15,20].
        self.assertEqual(occ.integral(0, 30), [20.0, 25.0])
        self.assertEqual(occ.integral(12, 18), [3.0, 9.0])

    def test_empty_and_inverted(self):
        occ = gap.Occupancy([[10, 20]], names=("busy",))
        self.assertEqual(occ.integral(20, 10), [0.0])
        self.assertEqual(occ.integral(25, 30), [0.0])


class TilingTest(unittest.TestCase):
    def test_contiguous(self):
        gap.check_tiling([("a", 0, 10_000, "x"), ("b", 10_000, 20_000, "y")])

    def test_gap_raises(self):
        with self.assertRaises(ValueError):
            gap.check_tiling([("a", 0, 10_000, "x"), ("b", 20_000, 30_000, "y")])

    def test_backwards_raises(self):
        with self.assertRaises(ValueError):
            gap.check_tiling([("a", 10_000, 0, "x")])


def pkg(name, asks, granted, released, height=0):
    return {"Pkg": name, "Height": height, "DepsDone": asks, "Enqueued": asks, "Granted": granted,
            "Acquired": granted, "Released": released}


class SlotSimTest(unittest.TestCase):
    def test_reproduces_a_measured_fifo_order(self):
        # One slot. A holds it 0..10; B then C ask at 5 and 6 with equal priority: B goes first.
        pk = {"A": pkg("A", 0, 0, 10), "B": pkg("B", 5, 10, 20), "C": pkg("C", 6, 20, 30)}
        end, start = slotsim.simulate({"A": [], "B": [], "C": []}, pk, {"A": 0, "B": 1, "C": 1}, 1)
        self.assertEqual(start, {"A": 0, "B": 10, "C": 20})
        self.assertEqual(end, 30)

    def test_freed_slot_goes_to_a_queued_waiter_before_dependents_ask(self):
        # One slot. A holds it 0..10; B waits from 5; D imports A and asks the moment A is
        # released, with a higher priority than B. gobuild hands A's slot to B first.
        pk = {"A": pkg("A", 0, 0, 10), "B": pkg("B", 5, 10, 20), "D": pkg("D", 10, 20, 25)}
        deps = {"A": [], "B": [], "D": ["A"]}
        end, start = slotsim.simulate(deps, pk, {"A": 0, "B": 1, "D": 9}, 1)
        self.assertEqual(start["B"], 10)
        self.assertEqual(start["D"], 20)

    def test_priority_wins_among_waiters(self):
        pk = {"A": pkg("A", 0, 0, 10), "B": pkg("B", 5, 20, 30), "C": pkg("C", 6, 10, 20)}
        end, start = slotsim.simulate({"A": [], "B": [], "C": []}, pk, {"A": 0, "B": 1, "C": 2}, 1)
        self.assertEqual(start, {"A": 0, "C": 10, "B": 20})

    def test_heights_follow_importers(self):
        h = slotsim.heights({"a": [], "b": ["a"], "c": ["b"]}, lambda p: {"a": 1, "b": 10, "c": 100}[p])
        self.assertEqual(h, {"a": 111, "b": 110, "c": 100})


class ShrinkTest(unittest.TestCase):
    def test_cut_is_capped_at_the_non_compile_time(self):
        cut, missing = slotsim.shrink_amounts({"a": 100, "b": 30}, {"a": 40, "b": 25}, 50)
        self.assertEqual(cut, {"a": 50, "b": 5})
        self.assertEqual(missing, [])

    def test_missing_compile_time_leaves_the_hold_alone(self):
        cut, missing = slotsim.shrink_amounts({"a": 100, "b": 30}, {"a": 40}, 50)
        self.assertEqual(cut, {"a": 50, "b": 0})
        self.assertEqual(missing, ["b"])

    def test_shorter_holds_free_the_slot_for_a_waiter(self):
        # One slot. A holds it 0..10, B waits from 5. Cutting A by 4 grants B at 6.
        pk = {"A": pkg("A", 0, 0, 10), "B": pkg("B", 5, 10, 20)}
        _, start = slotsim.simulate({"A": [], "B": []}, pk, {"A": 0, "B": 1}, 1, {"A": 4, "B": 0})
        self.assertEqual(start["B"], 6)


if __name__ == "__main__":
    unittest.main()
