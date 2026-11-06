<?php

use Shop\Cart;
use Shop\Wishlist;

function put(&$v): void
{
    $v = new Wishlist();
}

function keep($v): void
{
}

function rebindG(): void
{
    global $g;
    $g = new Wishlist();
}

function rebindQ(): void
{
    $GLOBALS['q'] = new Wishlist();
}

function byReference(): void
{
    $r = new Cart();
    put($r);
    $r->add('by-reference');
}

function builtinByReference(string $s): void
{
    $m = new Cart();
    preg_match('/./', $s, $m);
    $m->add('builtin');
}

function functionArgument(): void
{
    $a = new Cart();
    keep($a);
    $a->add('function-argument');
}

function methodArgument(): void
{
    $a = new Cart();
    (new Cart())->setNote($a);
    $a->add('method-argument');
}

function newArgument(): void
{
    $a = new Cart();
    new Cart($a);
    $a->add('new-argument');
}

function variableVariable(string $n): void
{
    $v = new Cart();
    $$n = new Wishlist();
    $v->add('variable-variable');
}

function extracted(): void
{
    $e = new Cart();
    extract(['e' => new Wishlist()]);
    $e->add('extract');
}

function included(): void
{
    $i = new Cart();
    include __DIR__ . '/rebind-i.php';
    $i->add('include');
}

function evaluated(): void
{
    $v = new Cart();
    eval('$v = new \\Shop\\Wishlist();');
    $v->add('eval');
}

function parsed(string $s): void
{
    $p = new Cart();
    parse_str($s);
    $p->add('parse-str');
}

function plain(): void
{
    $x = new Cart();
    $x->add('plain');
}

function mutated(): void
{
    $x = new Cart();
    $note = new Wishlist();
    $x->setNote($note);
    $x->add('mutated');
}

function referenceAssigned(): void
{
    $x = new Cart();
    $y = &$x;
    $y = new Wishlist();
    $x->add('reference-assign');
}

function foreachByReference(array $a): void
{
    $x = new Cart();
    foreach ($a as &$x) {
    }
    $x->add('foreach-by-reference');
}

function shortList(): void
{
    $x = new Cart();
    [$x] = [new Wishlist()];
    $x->add('short-list');
}

function keyedList(): void
{
    $x = new Cart();
    list('a' => $x) = ['a' => new Wishlist()];
    $x->add('keyed-list');
}

function caught(): void
{
    $x = new Cart();
    try {
        throw new \Exception();
    } catch (\Exception $x) {
    }
    $x->add('catch');
}

function useByReference(): void
{
    $x = new Cart();
    $f = function () use (&$x) {
        $x = new Wishlist();
    };
    $f();
    $x->add('use-by-reference');
}

function unsetVariable(): void
{
    $x = new Cart();
    unset($x);
    $x->add('unset');
}

function bracedVariable(): void
{
    $x = new Cart();
    ${'x'} = new Wishlist();
    $x->add('braced-variable');
}

function mbParsed(string $s): void
{
    $x = new Cart();
    mb_parse_str($s);
    $x->add('mb-parse-str');
}

function globalVariableVariable(string $n): void
{
    $x = new Cart();
    global $$n;
    $x->add('global-variable-variable');
}
