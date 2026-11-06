<?php

use Shop\Cart;

function basket(): void
{
    $basket = new Cart();
    $basket->add('kiwi');
    (new \Shop\Cart())->add('lime');
    $basket?->add('date');
    /* Корзина 🛒 */ $basket->add('emoji');

    $mixed = new Cart();
    $mixed = new \Shop\Wishlist();
    $mixed->add('nut');

    $early->add('yam');
    $early = new Cart();

    $arrow = fn () => $basket->add('pea');
    $closure = function () use ($basket) {
        $basket->add('oat');
    };

    $missing = new Cart();
    $missing->remove('rye');
}
