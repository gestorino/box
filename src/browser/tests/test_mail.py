"""The email a site sent, as the model reads it: links and codes numbered, never shown."""

import json

from concierge_worker.mail import view


def _raw(subject='Hi', text='', html=''):
    return json.dumps({'subject': subject, 'text': text, 'html': html})


def test_links_and_codes_are_numbered_in_order_and_kept_aside():
    shown, links, codes = view(_raw(
        'Welcome', 'Use 551204 or 7788', '<a href="https://x.test/a?t=1">Confirm</a> <a href="http://x.test/b">Stop mails</a>'))
    assert links == ['https://x.test/a?t=1', 'http://x.test/b']
    assert codes == ['551204', '7788']
    assert '[link 1: Confirm]' in shown and '[link 2: Stop mails]' in shown
    assert '[code 1]' in shown and '[code 2]' in shown
    assert 'x.test' not in shown and '551204' not in shown and shown.startswith('Subject: Welcome')


def test_the_text_comes_from_the_html_when_the_email_has_none():
    shown, _, codes = view(_raw(html='<p>Your code: <b>4321</b></p>'))
    assert codes == ['4321'] and 'Your code: [code 1]' in shown


def test_mailto_and_other_links_are_dropped():
    shown, links, _ = view(_raw(html='<a href="mailto:a@b.c">Write</a><a href="javascript:x()">X</a>'))
    assert links == [] and 'mailto' not in shown and 'javascript' not in shown


def test_an_unreadable_value_is_an_empty_email():
    assert view('not json') == ('Subject: \n\n', [], [])


def test_a_link_whose_words_are_its_url_shows_no_url():
    shown, links, _ = view(_raw(html='<p>Copy: <a href="https://x.test/c?t=s3cr3t">https://x.test/c?t=s3cr3t</a></p>'))
    assert links == ['https://x.test/c?t=s3cr3t'] and '[link 1]' in shown and 's3cr3t' not in shown


def test_a_bare_url_in_the_html_is_a_link_too():
    shown, links, _ = view(_raw(html='<p>Or open https://x.test/c?t=s3cr3t in your browser</p>'))
    assert links == ['https://x.test/c?t=s3cr3t'] and 's3cr3t' not in shown and '[link 1]' in shown


def test_with_a_text_part_the_listed_links_show_no_url_either():
    shown, _, _ = view(_raw(text='Welcome', html='<a href="https://x.test/c?t=s3cr3t">https://x.test/c?t=s3cr3t</a>'))
    assert 's3cr3t' not in shown and 'x.test' not in shown


def test_no_url_is_ever_shown_whatever_the_email_holds():
    shown, _, _ = view(_raw(subject='Go to https://x.test/a?t=1', text='See http://y.test/b?k=2',
                            html='<a href="https://z.test/">Visit www https://z.test/q?u=3</a>'))
    assert 'x.test' not in shown and 'y.test' not in shown and 'z.test' not in shown
