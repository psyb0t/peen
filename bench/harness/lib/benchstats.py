"""Small statistics for benchmark pass rates and measurements."""

import math

# z for a two-sided 95 percent interval.
Z_95 = 1.959964
PERCENTILE_95 = 0.95
MEDIAN = 0.5


def wilson_interval(passes, trials, z=Z_95):
    """Wilson score interval for a pass rate, as (low, high).

    It stays inside [0, 1] and behaves at 0 of n and n of n, where the normal
    approximation does not. No trials gives (0, 1): nothing is known.
    """
    if trials <= 0:
        return 0.0, 1.0
    rate = passes / trials
    denominator = 1 + z * z / trials
    centre = rate + z * z / (2 * trials)
    spread = z * math.sqrt(rate * (1 - rate) / trials + z * z / (4 * trials * trials))
    return (
        max(0.0, (centre - spread) / denominator),
        min(1.0, (centre + spread) / denominator),
    )


def percentile(values, fraction):
    """Linear-interpolated percentile of a list of numbers; None when empty."""
    ordered = sorted(value for value in values if value is not None)
    if not ordered:
        return None
    if len(ordered) == 1:
        return ordered[0]
    position = fraction * (len(ordered) - 1)
    lower = math.floor(position)
    upper = math.ceil(position)
    weight = position - lower
    return ordered[lower] * (1 - weight) + ordered[upper] * weight


def median(values):
    return percentile(values, MEDIAN)


def p95(values):
    return percentile(values, PERCENTILE_95)


def intervals_overlap(first, second):
    return first[0] <= second[1] and second[0] <= first[1]
