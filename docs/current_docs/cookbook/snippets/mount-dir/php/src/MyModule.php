<?php

declare(strict_types=1);

namespace DaggerModule;

use Dagger\Attribute\DaggerFunction;
use Dagger\Attribute\DaggerObject;
use Dagger\Attribute\Doc;
use Dagger\Container;
use Dagger\Directory;

use function Dagger\dag;

#[DaggerObject]
class MyModule
{
    #[DaggerFunction]
    #[Doc('Return a container with a mounted directory')]
    public function mountDirectory(Directory $source): Container
    {
        return dag()
            ->container()
            ->from('alpine:latest')
            ->withMountedDirectory('/src', $source);
    }
}
