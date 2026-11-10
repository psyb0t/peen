import pytest

import benchstats


@pytest.mark.parametrize(
    ("passes", "trials", "low", "high"),
    [
        (8, 10, 0.4902, 0.9433),
        (0, 5, 0.0, 0.4345),
        (5, 5, 0.5655, 1.0),
        (0, 0, 0.0, 1.0),
    ],
)
def test_wilson_interval(passes, trials, low, high):
    got_low, got_high = benchstats.wilson_interval(passes, trials)
    assert got_low == pytest.approx(low, abs=1e-3)
    assert got_high == pytest.approx(high, abs=1e-3)


def test_more_trials_narrow_the_interval():
    small = benchstats.wilson_interval(4, 5)
    large = benchstats.wilson_interval(40, 50)
    assert large[1] - large[0] < small[1] - small[0]


@pytest.mark.parametrize(
    ("first", "second", "overlap"),
    [
        ((0.1, 0.5), (0.4, 0.9), True),
        ((0.0, 0.3), (0.6, 1.0), False),
        ((0.0, 0.5), (0.5, 1.0), True),
    ],
)
def test_intervals_overlap(first, second, overlap):
    assert benchstats.intervals_overlap(first, second) is overlap


@pytest.mark.parametrize(
    ("values", "median", "p95"),
    [
        (list(range(1, 101)), 50.5, 95.05),
        ([None, 3, None, 1, 2], 2, 2.9),
        ([7], 7, 7),
        ([None], None, None),
        ([], None, None),
    ],
)
def test_percentiles(values, median, p95):
    if median is None:
        assert benchstats.median(values) is None
        assert benchstats.p95(values) is None
        return
    assert benchstats.median(values) == pytest.approx(median)
    assert benchstats.p95(values) == pytest.approx(p95)
