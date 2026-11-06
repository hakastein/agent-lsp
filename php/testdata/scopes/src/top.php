<?php

use Shop\Cart;

$x = new Cart();
$x->add('top-level');
(new Cart())->add('top-level-new');
$arrow = fn () => $x->add('top-level-arrow');
$closure = function () use ($x) {
    $x->add('top-level-closure');
};
$nested = fn () => fn () => $x->add('top-level-nested');
$own = function () {
    $x = new Cart();
    $x->add('closure-own');
};

function body(): void
{
    $x = new Cart();
    $x->add('function-body');
    (new Cart())->add('function-new');
    $arrow = fn () => $x->add('function-arrow');
    $closure = function () use ($x) {
        $x->add('function-closure');
    };
}

function nested(): void
{
    $x = new Cart();
    $arrows = fn () => fn () => $x->add('nested-arrows');
    $mixed = fn () => function () use ($x) {
        $x->add('closure-in-arrow');
    };
}

class Till
{
    public function ring(): void
    {
        $x = new Cart();
        $x->add('method-body');
    }
}
