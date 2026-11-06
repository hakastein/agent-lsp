<?php

namespace Shop;

function enums(): bool
{
    $e = new Enum();
    $e->label('en');
    $m = new Method();
    $m->label('fr');
    $name = Enum::class;
    return $e instanceof Enum;
}
