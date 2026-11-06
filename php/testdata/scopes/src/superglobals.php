<?php

use Shop\Cart;
use Shop\Wishlist;

function wipe(): void
{
    $_SESSION = new Wishlist();
}

$_SESSION = new Cart();
wipe();
$_SESSION->add('session-top-level');

function session(): void
{
    $_SESSION = new Cart();
    wipe();
    $_SESSION->add('session-function');
}

function superglobals(): void
{
    $_SERVER = new Cart();
    $_SERVER->add('server');
    $_GET = new Cart();
    $_GET->add('get');
    $_POST = new Cart();
    $_POST->add('post');
    $_FILES = new Cart();
    $_FILES->add('files');
    $_COOKIE = new Cart();
    $_COOKIE->add('cookie');
    $_REQUEST = new Cart();
    $_REQUEST->add('request');
    $_ENV = new Cart();
    $_ENV->add('env');
}

function locals(): void
{
    $session = new Cart();
    $session->add('local-session');
    $_session = new Cart();
    $_session->add('lower-case-session');
}
