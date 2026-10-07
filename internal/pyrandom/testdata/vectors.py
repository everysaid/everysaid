"""Writes vectors.json, CPython's answers that pyrandom_test.go checks: python vectors.py > vectors.json"""
import json
import random
import sys

out = []
for seed in (0, 1, 7, 42, 7001, 7315, -5, 2**32 + 3, 2**63 - 1, 123456789012):
    r = random.Random(seed)
    v = {"seed": seed}
    v["random"] = [r.random() for _ in range(5)]
    v["bits"] = [[k, r.getrandbits(k)] for k in (1, 5, 31, 32, 33, 53, 64)]
    v["randbelow"] = [[n, r._randbelow(n)] for n in (1, 2, 3, 5, 7, 26, 100, 1000, 2**31 + 7, 1576800)]
    v["randrange"] = [[a, b, r.randrange(a, b)] for a, b in ((0, 700), (20, 60), (40, 220), (-10, 10), (300, 900))]
    v["randrange_step"] = [[a, b, s, r.randrange(a, b, s)] for a, b, s in ((0, 100, 7), (100, 0, -3), (5, 6, 2))]
    v["randint"] = [[a, b, r.randint(a, b)] for a, b in ((1, 6), (0, 0), (-3, 3))]
    v["choice"] = [[n, r.choice(range(n))] for n in (1, 4, 8, 26)]
    v["sample"] = [[n, k, r.sample(range(n), k)] for n, k in ((5, 1), (5, 3), (26, 6), (26, 8), (100, 6), (1000, 30), (40, 4), (22, 2))]
    x = list(range(20))
    r.shuffle(x)
    v["shuffle"] = x
    v["choices"] = r.choices(range(10), k=6)
    v["choices_w"] = r.choices(range(5), weights=[1, 0, 2.5, 3, 8], k=8)
    v["uniform"] = [r.uniform(-2.5, 7.25) for _ in range(3)]
    out.append(v)
json.dump(out, sys.stdout, indent=0)
