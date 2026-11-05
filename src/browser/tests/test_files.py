"""`files.py` — where the worker keeps what it writes, under the roots it was given, and the budget a
run's recording keeps. A site key reaches here from the engine, so a key that is not one path
element deletes and makes nothing."""

import os
import stat

import pytest

from concierge_worker.files import (RUN_BUDGET, bytes_left, forget_profile, forget_runs, is_site, log_path,
                                    part_path, profile_dir, run_dir)


def _mode(path) -> int:
    return stat.S_IMODE(os.stat(path).st_mode)


def test_a_site_key_is_one_lower_case_path_element():
    assert is_site('example.com') and is_site('shop.co.uk') and is_site('xn--d1acufc.xn--p1ai')
    for bad in ('', '.', '..', '../x', 'a/b', 'A.com', 'a\\b', 'example.com\n', 'a b', None, 7):
        assert not is_site(bad), bad


def test_a_runs_files_are_under_its_site_and_run(tmp_path):
    r = str(tmp_path / 'recordings')
    assert part_path(r, 'example.com', 7, 2).endswith('/example.com/7/2.mp4')
    assert log_path(r, 'example.com', 7).endswith('/example.com/7/browser-use.log')
    assert run_dir(r, 'example.com', 7) == os.path.join(r, 'example.com', '7')


def test_made_run_dirs_are_the_groups_to_read(tmp_path):
    r = tmp_path / 'recordings'
    part_path(str(r), 'example.com', 7, 1)
    for d in (r, r / 'example.com', r / 'example.com' / '7'):
        assert _mode(d) == 0o750, d


def test_a_profile_is_the_workers_alone(tmp_path):
    p = tmp_path / 'profiles'
    assert profile_dir(str(p), 'example.com') == str(p / 'example.com')
    assert _mode(p) == 0o700 and _mode(p / 'example.com') == 0o700


def test_the_budget_is_what_the_run_has_not_written(tmp_path):
    r = str(tmp_path / 'recordings')
    assert RUN_BUDGET == 100 * 1024 * 1024
    assert bytes_left(r, 'example.com', 7) == RUN_BUDGET           # nothing made yet
    d = run_dir(r, 'example.com', 7)
    assert bytes_left(r, 'example.com', 7) == RUN_BUDGET           # an empty directory
    with open(os.path.join(d, '1.mp4'), 'wb') as f:
        f.truncate(1000)
    assert bytes_left(r, 'example.com', 7) == RUN_BUDGET - 1000
    with open(os.path.join(d, '2.mp4'), 'wb') as f:
        f.truncate(RUN_BUDGET)
    assert bytes_left(r, 'example.com', 7) == 0                    # never below nothing


def test_forgetting_runs_removes_only_those_runs(tmp_path):
    r = str(tmp_path / 'recordings')
    for run in (7, 8):
        open(part_path(r, 'example.com', run, 1), 'w').close()
    forget_runs(r, 'example.com', [7])
    assert not os.path.exists(os.path.join(r, 'example.com', '7'))
    assert os.path.exists(os.path.join(r, 'example.com', '8', '1.mp4'))
    forget_runs(r, 'example.com', [7, 9])                           # gone already, or never made


def test_forgetting_a_profile_removes_it(tmp_path):
    p = str(tmp_path / 'profiles')
    open(os.path.join(profile_dir(p, 'example.com'), 'Cookies'), 'w').close()
    forget_profile(p, 'example.com')
    assert not os.path.exists(os.path.join(p, 'example.com'))
    forget_profile(p, 'example.com')                                # nothing left is no error


def test_a_bad_key_deletes_nothing(tmp_path):
    victim = tmp_path / 'x'
    victim.mkdir()
    (victim / 'keep').write_text('')
    root = tmp_path / 'profiles'
    root.mkdir()
    for bad in ('../x', '..', '.', '', 'a/b'):
        with pytest.raises(ValueError):
            forget_profile(str(root), bad)
        with pytest.raises(ValueError):
            forget_runs(str(root), bad, [1])
    assert (victim / 'keep').exists() and root.exists()


def test_a_run_is_a_number(tmp_path):
    r = str(tmp_path / 'recordings')
    os.makedirs(os.path.join(r, 'example.com', '7'))
    for bad in ('7', '../..', -1, True, 1.0):
        with pytest.raises(ValueError):
            forget_runs(r, 'example.com', [bad])
        with pytest.raises(ValueError):
            part_path(r, 'example.com', bad, 1)
    # One bad run deletes none of the good ones before it.
    with pytest.raises(ValueError):
        forget_runs(r, 'example.com', [7, '..'])
    assert os.path.exists(os.path.join(r, 'example.com', '7'))
